/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'

import type {
  AutoscalingInstancesResult,
  AutoscalingTestResult,
} from './auto-scaling-lib'

export type AutoscalingTestParams = {
  token: string
}

/**
 * Test the connection to the AutoDL auto-scaling backend.
 * The API token is sent in the POST JSON body; the backend prefers this
 * in-form (possibly unsaved) token over the persisted one for the test.
 */
export async function getAutoscalingTest(
  params: AutoscalingTestParams
): Promise<AutoscalingTestResult> {
  // skipBusinessError keeps the shared success-response interceptor from
  // toasting the failure here; the component's onSuccess/onError handlers
  // surface the outcome once.
  const res = await api.post<AutoscalingTestResult>(
    '/api/autoscaling/test',
    {
      token: params.token,
    },
    {
      skipBusinessError: true,
    }
  )
  return res.data
}

/**
 * List the AutoDL instances available for auto-scaling.
 * Uses the persisted AutoDL token server-side. The endpoint reports "未配置
 * AutoDL Token" (success: false) as a business error without an HTTP error
 * status, so skipBusinessError keeps the shared interceptor from toasting it;
 * the caller surfaces load failures itself.
 */
export async function getAutoscalingInstances(): Promise<AutoscalingInstancesResult> {
  const res = await api.get<AutoscalingInstancesResult>(
    '/api/autoscaling/instances',
    {
      skipBusinessError: true,
    }
  )
  return res.data
}