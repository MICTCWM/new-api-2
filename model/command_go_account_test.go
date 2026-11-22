package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCommandGoAccountTestDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&CommandGoAccount{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestSelectCommandGoAccountRotatesEvenInSameSecond(t *testing.T) {
	setupCommandGoAccountTestDB(t)

	previousNow := commandGoNow
	fixed := time.Unix(1_700_000_000, 0)
	commandGoNow = func() time.Time { return fixed }
	t.Cleanup(func() { commandGoNow = previousNow })

	for i := 1; i <= 3; i++ {
		account := &CommandGoAccount{
			ChannelId:      1,
			Name:           fmt.Sprintf("acct-%d", i),
			ApiKey:         fmt.Sprintf("user_rota%04d", i),
			Status:         CommandGoAccountStatusActive,
			QuotaAvailable: true,
		}
		require.NoError(t, DB.Create(account).Error)
	}

	counts := map[string]int{}
	var stamps []int64
	for i := 0; i < 6; i++ {
		selected, err := SelectCommandGoAccount(1)
		require.NoError(t, err)
		require.NotNil(t, selected)
		counts[selected.Name]++
		stamps = append(stamps, selected.LastUsedAt)
	}
	for name, count := range counts {
		assert.Equal(t, 2, count, name)
	}
	for i := 1; i < len(stamps); i++ {
		assert.GreaterOrEqual(t, stamps[i], stamps[i-1], "cursor must never move backwards")
	}

	// 第 N+1 次选择开启新一轮，分布仍保持均匀。
	selected, err := SelectCommandGoAccount(1)
	require.NoError(t, err)
	require.NotNil(t, selected)
	counts[selected.Name]++
	assert.Equal(t, 3, counts["acct-1"])
	assert.Equal(t, 2, counts["acct-2"])
	assert.Equal(t, 2, counts["acct-3"])
}
