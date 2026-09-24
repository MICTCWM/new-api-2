// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"errors"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// ModelSplitUsage stores the 100-call window remaining counts for one
// (rule_id, group) pair. Remaining1/Remaining2 can go negative only for the
// image-forced side (debt); RemainingTotal is not reduced for those debts.
type ModelSplitUsage struct {
	Id             int    `json:"id"`
	RuleId         string `json:"rule_id" gorm:"size:64;uniqueIndex:uk_model_split_rule_group"`
	GroupName      string `json:"group" gorm:"column:group_name;size:128;uniqueIndex:uk_model_split_rule_group"`
	Remaining1     int    `json:"remaining_model_1"`
	Remaining2     int    `json:"remaining_model_2"`
	RemainingTotal int    `json:"remaining_total"`
}

type ModelSplitReservation struct {
	RuleId      string
	GroupName   string
	Side        int
	DeductTotal bool
	TargetModel string
	Committed   bool
}

var modelSplitLocks sync.Map // string -> *sync.Mutex

func modelSplitLockKey(ruleId, groupName string) string {
	return ruleId + "\x00" + groupName
}

func lockModelSplitKey(ruleId, groupName string) func() {
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return func() {}
	}
	key := modelSplitLockKey(ruleId, groupName)
	val, _ := modelSplitLocks.LoadOrStore(key, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func decideModelSplit(remaining1, remaining2, remainingTotal, percent1, percent2, forceImageSide int) (int, int, int, int, bool) {
	if max(remaining1, 0)+max(remaining2, 0) == 0 {
		remaining1 += percent1
		remaining2 += percent2
		remainingTotal += operation_setting.ModelSplitWindowSize
	}

	side := 1
	if forceImageSide == 1 || forceImageSide == 2 {
		side = forceImageSide
	} else {
		side = chooseNoImageSplitSide(remaining1, remaining2, percent1, percent2)
	}

	remaining := remaining1
	if side == 2 {
		remaining = remaining2
	}
	deductTotal := remaining > 0 || forceImageSide == 0
	if side == 1 {
		remaining1--
	} else {
		remaining2--
	}
	if deductTotal {
		remainingTotal--
	}
	return remaining1, remaining2, remainingTotal, side, deductTotal
}

func chooseNoImageSplitSide(remaining1, remaining2, percent1, percent2 int) int {
	eligible1 := percent1 > 0 && remaining1 > 0
	eligible2 := percent2 > 0 && remaining2 > 0
	switch {
	case eligible1 && eligible2:
		if remaining2*percent1 > remaining1*percent2 {
			return 2
		}
		return 1
	case eligible1:
		return 1
	case eligible2:
		return 2
	}
	// No side has both weight and balance at once (theoretically unreachable
	// after refill). Never route a normal request into a debt side while the
	// other side still has balance; a percent==0 side is only picked as a last
	// resort when it is the sole side with balance.
	if remaining1 > 0 {
		return 1
	}
	if remaining2 > 0 {
		return 2
	}
	if percent1 > 0 && percent2 > 0 {
		if remaining2 > remaining1 {
			return 2
		}
		return 1
	}
	if percent2 > 0 {
		return 2
	}
	return 1
}

func getOrCreateModelSplitUsageTx(tx *gorm.DB, ruleId, groupName string, percent1, percent2 int) (*ModelSplitUsage, error) {
	var usage ModelSplitUsage
	err := lockForUpdate(tx).Where("rule_id = ? AND group_name = ?", ruleId, groupName).First(&usage).Error
	if err == nil {
		return &usage, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	usage = ModelSplitUsage{
		RuleId:         ruleId,
		GroupName:      groupName,
		Remaining1:     percent1,
		Remaining2:     percent2,
		RemainingTotal: operation_setting.ModelSplitWindowSize,
	}
	if createErr := tx.Create(&usage).Error; createErr != nil {
		if retryErr := lockForUpdate(tx).Where("rule_id = ? AND group_name = ?", ruleId, groupName).First(&usage).Error; retryErr != nil {
			return nil, createErr
		}
	}
	return &usage, nil
}

func ReserveModelSplitUsage(ruleId, groupName string, percent1, percent2, forceImageSide int) (*ModelSplitReservation, error) {
	if ruleId == "" || groupName == "" {
		return nil, errors.New("model split reservation is missing rule or group")
	}
	unlock := lockModelSplitKey(ruleId, groupName)
	defer unlock()

	var reservation *ModelSplitReservation
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateModelSplitUsageTx(tx, ruleId, groupName, percent1, percent2)
		if err != nil {
			return err
		}
		remaining1, remaining2, remainingTotal, side, deductTotal := decideModelSplit(
			usage.Remaining1, usage.Remaining2, usage.RemainingTotal, percent1, percent2, forceImageSide,
		)
		usage.Remaining1 = remaining1
		usage.Remaining2 = remaining2
		usage.RemainingTotal = remainingTotal
		if err := tx.Save(usage).Error; err != nil {
			return err
		}
		reservation = &ModelSplitReservation{
			RuleId:      ruleId,
			GroupName:   groupName,
			Side:        side,
			DeductTotal: deductTotal,
		}
		return nil
	})
	return reservation, err
}

func RefundModelSplitUsage(reservation *ModelSplitReservation) error {
	if reservation == nil || reservation.Committed {
		return nil
	}
	if reservation.RuleId == "" || reservation.GroupName == "" {
		return nil
	}
	unlock := lockModelSplitKey(reservation.RuleId, reservation.GroupName)
	defer unlock()

	return DB.Transaction(func(tx *gorm.DB) error {
		var usage ModelSplitUsage
		err := lockForUpdate(tx).Where("rule_id = ? AND group_name = ?", reservation.RuleId, reservation.GroupName).First(&usage).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if reservation.Side == 2 {
			usage.Remaining2++
		} else {
			usage.Remaining1++
		}
		if reservation.DeductTotal {
			usage.RemainingTotal++
		}
		return tx.Save(&usage).Error
	})
}

func ListModelSplitUsages() ([]ModelSplitUsage, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var rows []ModelSplitUsage
	if err := DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func GetModelSplitUsage(ruleId, groupName string) (*ModelSplitUsage, error) {
	var usage ModelSplitUsage
	err := DB.Where("rule_id = ? AND group_name = ?", ruleId, groupName).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &usage, nil
}
