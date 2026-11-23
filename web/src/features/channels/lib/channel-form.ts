import { z } from 'zod'

import {
  CHANNEL_TYPE_COMMAND,
  CHANNEL_TYPE_COMMAND_GO,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_STATUS,
  getFixedCodeChannelModels,
  CLAUDE_FIELD_PASSTHROUGH_TYPES,
  ERROR_MESSAGES,
  FIELD_PASSTHROUGH_TYPES,
  getFixedCodeChannelModel,
  MODEL_FETCHABLE_TYPES,
  OPENAI_FIELD_PASSTHROUGH_TYPES,
  SPEED_PROTECTION_SUPPORTED_CHANNEL_TYPES,
  isFixedCodeChannelType,
} from '../constants'
import type { Channel } from '../types'
import {
  CHANNEL_TYPE_ADVANCED_CUSTOM,
  advancedCustomConfigUsesRelativeUpstreamPath,
  hasValidAdvancedCustomModelListRoute,
  parseAdvancedCustomConfig,
  stringifyAdvancedCustomConfig,
  validateAdvancedCustomConfig,
} from './advanced-custom'
import {
  extractMappingSourceModels,
  formatModelsArray,
  parseModelsString,
} from './model-mapping-validation'

// ============================================================================
// Form Validation Schema
// ============================================================================

const SUPPORTED_PROXY_PROTOCOLS = new Set([
  'http:',
  'https:',
  'socks5:',
  'socks5h:',
])

function fixedCodeChannelBaseUrl(type: number): string {
  if (type === CHANNEL_TYPE_COMMAND) {
    return 'https://api.commandcode.ai/provider/v1'
  }
  if (type === CHANNEL_TYPE_COMMAND_GO) {
    return 'https://api.commandcode.ai/alpha'
  }
  return 'https://opencode.ai/zen/go/v1/chat/completions'
}

function fixedCodeChannelModels(
  type: number,
  models: string,
  modelMapping: string | undefined
): string {
  const nativeModels = getFixedCodeChannelModels(type)
  const fixedModel = nativeModels[0]
  const mappingSources = new Set(extractMappingSourceModels(modelMapping || ''))
  const allowedModels = new Set([...nativeModels, ...mappingSources])
  const selectedModels = parseModelsString(models).filter((model) =>
    allowedModels.has(model)
  )
  if (type === CHANNEL_TYPE_COMMAND) {
    // v4-pro 为可选附加模型；v4-flash 与 v4.1-flash 均可自由保留或移除，
    // 只要渠道至少包含 Flash 或 V4.1 Flash 之一。
    const result = [...selectedModels]
    const hasPrimary =
      result.includes(fixedModel) ||
      result.includes('deepseek/deepseek-v4.1-flash')
    if (!hasPrimary) {
      result.unshift(fixedModel)
    }
    return formatModelsArray(result)
  }
  if (type === CHANNEL_TYPE_COMMAND_GO) {
    // Command Go 仅有 v4.1 Flash 一个原生模型，缺省时补回。
    if (selectedModels.includes(fixedModel)) {
      return formatModelsArray(selectedModels)
    }
    return formatModelsArray([fixedModel, ...selectedModels])
  }
  // OpenCode allows any of its native models (flash, glm, v4.1, pro).
  const hasNative = selectedModels.some((model) =>
    (nativeModels as readonly string[]).includes(model)
  )
  if (hasNative) {
    return formatModelsArray(selectedModels)
  }
  return formatModelsArray([fixedModel, ...selectedModels])
}

function isOptionalProxyURL(value: string | undefined): boolean {
  const trimmedValue = value?.trim() || ''
  if (!trimmedValue) return true

  const schemeSeparatorIndex = trimmedValue.indexOf('://')
  if (schemeSeparatorIndex <= 0) return false

  const authorityAndSuffix = trimmedValue.slice(schemeSeparatorIndex + 3)
  const suffixIndex = authorityAndSuffix.search(/[/?#]/)
  if (suffixIndex >= 0 && authorityAndSuffix.slice(suffixIndex) !== '/') {
    return false
  }

  try {
    const parsedURL = new URL(trimmedValue)
    return (
      SUPPORTED_PROXY_PROTOCOLS.has(parsedURL.protocol) &&
      Boolean(parsedURL.hostname) &&
      parsedURL.port !== '0'
    )
  } catch {
    return false
  }
}

export const HTTP_PROTOCOL_AUTO = 'auto'
export const HTTP_PROTOCOL_HTTP1 = 'http1'
export const MAX_HTTP2_CONNECTION_SHARDS = 8

export function normalizeHttpProtocol(
  value: string | undefined | null
): 'auto' | 'http1' {
  const normalized = String(value || '')
    .trim()
    .toLowerCase()
  if (normalized === HTTP_PROTOCOL_HTTP1) {
    return HTTP_PROTOCOL_HTTP1
  }
  return HTTP_PROTOCOL_AUTO
}

export function normalizeHttp2ConnectionShards(
  value: number | undefined | null
): number {
  if (value == null || Number.isNaN(value) || value === 0) {
    return 1
  }
  if (value < 1) {
    return 1
  }
  if (value > MAX_HTTP2_CONNECTION_SHARDS) {
    return MAX_HTTP2_CONNECTION_SHARDS
  }
  return value
}

function parseOptionalJson(value: string | undefined): unknown {
  if (!value?.trim()) return undefined
  return JSON.parse(value)
}

function isJsonObjectValue(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isOptionalJsonObject(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    return parsed === undefined || isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function isOptionalModelMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.values(parsed).every((item) => typeof item === 'string')
  } catch {
    return false
  }
}

function isOptionalStatusCodeMapping(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (!isJsonObjectValue(parsed)) return false
    return Object.entries(parsed).every(([from, to]) => {
      const fromCode = Number(from)
      const toCode = Number(to)
      return (
        Number.isInteger(fromCode) &&
        Number.isInteger(toCode) &&
        fromCode >= 100 &&
        fromCode <= 599 &&
        toCode >= 100 &&
        toCode <= 599
      )
    })
  } catch {
    return false
  }
}

function isCodexCredential(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    return (
      isJsonObjectValue(parsed) &&
      typeof parsed.access_token === 'string' &&
      parsed.access_token.trim().length > 0 &&
      typeof parsed.account_id === 'string' &&
      parsed.account_id.trim().length > 0
    )
  } catch {
    return false
  }
}

function isVertexJsonKey(value: string | undefined): boolean {
  try {
    const parsed = parseOptionalJson(value)
    if (parsed === undefined) return true
    if (Array.isArray(parsed)) {
      return parsed.every((item) => isJsonObjectValue(item))
    }
    return isJsonObjectValue(parsed)
  } catch {
    return false
  }
}

function addRequiredIssue(
  ctx: z.RefinementCtx,
  path: string,
  message: string
): void {
  ctx.addIssue({
    code: z.ZodIssueCode.custom,
    path: [path],
    message,
  })
}

export const channelFormSchema = z
  .object({
    name: z.string().min(1, ERROR_MESSAGES.REQUIRED_NAME),
    type: z.number().min(0, ERROR_MESSAGES.REQUIRED_TYPE),
    base_url: z.string().optional(),
    key: z.string(),
    openai_organization: z.string().optional(),
    models: z.string().min(1, ERROR_MESSAGES.REQUIRED_MODELS),
    group: z.array(z.string()).min(1, ERROR_MESSAGES.REQUIRED_GROUP),
    model_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalModelMapping,
        'Model mapping must be a JSON object with string values'
      ),
    priority: z.number().optional(),
    weight: z.number().optional(),
    test_model: z.string().optional(),
    auto_ban: z.number().optional(),
    fallback_sequence: z.string().optional(),
    status: z.number(),
    status_code_mapping: z
      .string()
      .optional()
      .refine(
        isOptionalStatusCodeMapping,
        'Status code mapping must use valid HTTP status codes'
      ),
    tag: z.string().optional(),
    remark: z
      .string()
      .max(255, 'Remark must be less than 255 characters')
      .optional(),
    setting: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    param_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    header_override: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    settings: z
      .string()
      .optional()
      .refine(isOptionalJsonObject, ERROR_MESSAGES.INVALID_JSON),
    advanced_custom: z.string().optional(),
    other: z.string().optional(),
    // Multi-key options (not sent to backend directly)
    multi_key_mode: z.enum(['single', 'batch', 'multi_to_single']).optional(),
    multi_key_type: z.enum(['random', 'polling']).optional(),
    batch_add_set_key_prefix_2_name: z.boolean().optional(),
    key_mode: z.enum(['append', 'replace']).optional(), // For editing multi-key channels
    // Channel extra settings (stored in setting JSON, not sent directly)
    force_format: z.boolean().optional(),
    thinking_to_content: z.boolean().optional(),
    proxy: z
      .string()
      .optional()
      .refine(isOptionalProxyURL, ERROR_MESSAGES.INVALID_PROXY),
    http_protocol: z.enum(['auto', 'http1']).optional(),
    http2_connection_shards: z.number().int().optional(),
    pass_through_body_enabled: z.boolean().optional(),
    gzip_enabled: z.boolean().optional(),
    system_prompt: z.string().optional(),
    system_prompt_override: z.boolean().optional(),
    // Type-specific settings (stored in settings JSON)
    channel_concurrency_limit: z.number().int().min(0).max(10000).optional(),
    channel_retry_times: z.number().int().min(0).max(5).optional(),
    fallback_retry_enabled: z.boolean().optional(),
    channel_tpm_limit: z.number().int().min(0).max(100_000_000).optional(),
    channel_tpm_buffer: z.number().int().min(0).max(100_000_000).optional(),
    channel_limit_mode: z.enum(['', 'reject', 'queue']).optional(),
    rewrite_upstream_429: z.boolean().optional(),
    speed_protection_enabled: z.boolean().optional(),
    speed_protection_groups: z.array(z.string()).optional(),
    is_enterprise_account: z.boolean().optional(), // OpenRouter specific
    vertex_key_type: z.enum(['json', 'api_key']).optional(), // Vertex AI specific
    aws_key_type: z.enum(['ak_sk', 'api_key']).optional(), // AWS specific
    azure_responses_version: z.string().optional(), // Azure specific
    // Field passthrough controls (stored in settings JSON)
    allow_service_tier: z.boolean().optional(), // OpenAI/Anthropic
    disable_store: z.boolean().optional(), // OpenAI only
    allow_safety_identifier: z.boolean().optional(), // OpenAI only
    allow_include_obfuscation: z.boolean().optional(), // OpenAI: include usage obfuscation
    allow_inference_geo: z.boolean().optional(), // OpenAI/Anthropic: inference geography
    allow_speed: z.boolean().optional(), // Anthropic: speed mode control
    claude_beta_query: z.boolean().optional(), // Anthropic: beta query passthrough
    disable_task_polling_sleep: z.boolean().optional(),
    station_cache_enabled: z.boolean().optional(), // Gateway station cache billing discount
    simulated_cache_creation_enabled: z.boolean().optional(), // Locally simulate upstream cache creation for billing display only
    fake_non_stream_enabled: z.boolean().optional(), // Client gets non-stream JSON while upstream is called with stream=true
    video_frame_extraction_enabled: z.boolean().optional(), // Extract Chat/Responses video input into image frames
    disable_protocol_conversion: z.boolean().optional(), // Keep Chat/Responses requests in their original protocol
    // Upstream model update settings (stored in settings JSON)
    upstream_model_update_check_enabled: z.boolean().optional(),
    upstream_model_update_auto_sync_enabled: z.boolean().optional(),
    upstream_model_update_ignored_models: z.string().optional(),
  })
  .superRefine((data, ctx) => {
    if (
      [3, 8, 36, 45, CHANNEL_TYPE_NEW_API].includes(data.type) &&
      !data.base_url?.trim()
    ) {
      addRequiredIssue(
        ctx,
        'base_url',
        'Base URL is required for this channel type'
      )
    }

    if (data.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
      const advancedCustomConfig = parseAdvancedCustomConfig(
        data.advanced_custom
      )
      const advancedCustomError =
        validateAdvancedCustomConfig(advancedCustomConfig)
      if (advancedCustomError) {
        addRequiredIssue(ctx, 'advanced_custom', advancedCustomError.message)
      }
      if (
        advancedCustomConfigUsesRelativeUpstreamPath(advancedCustomConfig) &&
        !data.base_url?.trim()
      ) {
        addRequiredIssue(
          ctx,
          'base_url',
          'Base URL is required when an advanced route uses an upstream path'
        )
      }
      if (
        data.upstream_model_update_check_enabled === true &&
        !hasValidAdvancedCustomModelListRoute(advancedCustomConfig)
      ) {
        addRequiredIssue(
          ctx,
          'upstream_model_update_check_enabled',
          'OpenAI Models route is required to enable upstream model checks'
        )
      }
    }

    if ([3, 18, 21, 39, 41, 49].includes(data.type) && !data.other?.trim()) {
      addRequiredIssue(
        ctx,
        'other',
        'This channel type requires additional configuration'
      )
    }

    if (isFixedCodeChannelType(data.type)) {
      if (data.type === CHANNEL_TYPE_COMMAND_GO) {
        // Command Go is one channel + one account pool; batch creation and
        // multi-key modes are rejected by the backend.
        if (data.multi_key_mode && data.multi_key_mode !== 'single') {
          addRequiredIssue(
            ctx,
            'multi_key_mode',
            'Command Go channels only support single key creation'
          )
        }
      } else if (data.multi_key_mode === 'multi_to_single') {
        addRequiredIssue(
          ctx,
          'multi_key_mode',
          'Fixed code channels require batch mode for multiple API keys'
        )
      }
      const configuredModels = parseModelsString(data.models)
      const mappingSources = new Set(
        extractMappingSourceModels(data.model_mapping || '')
      )
      const invalidModels = configuredModels.filter(
        (model) =>
          !getFixedCodeChannelModels(data.type).includes(model) &&
          !mappingSources.has(model)
      )
      let fixedModelError =
        'Opencode supports deepseek-v4-flash, glm-5.3-flash, deepseek-v4.1-flash, deepseek-v4-pro, and model mapping source models'
      if (data.type === CHANNEL_TYPE_COMMAND) {
        fixedModelError =
          'Command supports Flash, Pro, V4.1 Flash, and model mapping source models'
      } else if (data.type === CHANNEL_TYPE_COMMAND_GO) {
        fixedModelError =
          'Command Go supports deepseek/deepseek-v4.1-flash and model mapping source models'
      }
      if (invalidModels.length > 0) {
        addRequiredIssue(ctx, 'models', fixedModelError)
      }
      if (data.type === CHANNEL_TYPE_COMMAND) {
        // Command 必须包含 Flash 或 V4.1 Flash 之一；Pro 为可选附加模型。
        const hasPrimaryModel =
          configuredModels.includes(getFixedCodeChannelModel(data.type)) ||
          configuredModels.includes('deepseek/deepseek-v4.1-flash')
        if (!hasPrimaryModel) {
          addRequiredIssue(
            ctx,
            'models',
            'Command must include deepseek/deepseek-v4-flash or deepseek/deepseek-v4.1-flash'
          )
        }
      } else if (data.type === CHANNEL_TYPE_COMMAND_GO) {
        if (!configuredModels.includes(getFixedCodeChannelModel(data.type))) {
          addRequiredIssue(
            ctx,
            'models',
            'Command Go must include deepseek/deepseek-v4.1-flash'
          )
        }
      } else {
        const hasNativeModel = configuredModels.some((model) =>
          (getFixedCodeChannelModels(data.type) as readonly string[]).includes(
            model
          )
        )
        if (!hasNativeModel) {
          addRequiredIssue(
            ctx,
            'models',
            'Opencode must include deepseek-v4-flash, glm-5.3-flash, deepseek-v4.1-flash, or deepseek-v4-pro'
          )
        }
      }
    }

    if (data.speed_protection_enabled) {
      if (!SPEED_PROTECTION_SUPPORTED_CHANNEL_TYPES.has(data.type)) {
        addRequiredIssue(
          ctx,
          'speed_protection_enabled',
          'Speed protection is not supported for this channel type'
        )
      }
      if ((data.speed_protection_groups || []).length === 0) {
        addRequiredIssue(
          ctx,
          'speed_protection_groups',
          'Select at least one protected group'
        )
      }
      const channelGroups = new Set(data.group)
      const invalidGroups = (data.speed_protection_groups || []).filter(
        (group) => !channelGroups.has(group)
      )
      if (invalidGroups.length > 0) {
        addRequiredIssue(
          ctx,
          'speed_protection_groups',
          'Protected groups must belong to this channel'
        )
      }
    }

    if (data.type === 57) {
      if (data.multi_key_mode && data.multi_key_mode !== 'single') {
        addRequiredIssue(
          ctx,
          'multi_key_mode',
          'Codex channels do not support batch creation'
        )
      }
      if (data.key?.trim() && !isCodexCredential(data.key)) {
        addRequiredIssue(
          ctx,
          'key',
          'Codex credential must be a JSON object with access_token and account_id'
        )
      }
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'json' &&
      data.key?.trim() &&
      !isVertexJsonKey(data.key)
    ) {
      addRequiredIssue(
        ctx,
        'key',
        'Vertex AI service account key must be valid JSON'
      )
    }

    if (
      data.type === 41 &&
      data.vertex_key_type === 'api_key' &&
      data.multi_key_mode &&
      data.multi_key_mode !== 'single'
    ) {
      addRequiredIssue(
        ctx,
        'multi_key_mode',
        'Vertex AI API Key mode does not support batch creation'
      )
    }

    const protocol = normalizeHttpProtocol(data.http_protocol)
    const shards = data.http2_connection_shards ?? 1
    if (shards < 1 || shards > MAX_HTTP2_CONNECTION_SHARDS) {
      addRequiredIssue(
        ctx,
        'http2_connection_shards',
        ERROR_MESSAGES.INVALID_HTTP2_CONNECTION_SHARDS
      )
    }
    if (protocol === HTTP_PROTOCOL_HTTP1 && shards > 1) {
      addRequiredIssue(
        ctx,
        'http2_connection_shards',
        ERROR_MESSAGES.INVALID_HTTP1_WITH_SHARDS
      )
    }
  })

export type ChannelFormValues = z.infer<typeof channelFormSchema>

// ============================================================================
// Default Form Values
// ============================================================================

export const CHANNEL_FORM_DEFAULT_VALUES: ChannelFormValues = {
  name: '',
  type: 1,
  base_url: '',
  key: '',
  openai_organization: '',
  models: '',
  group: ['default'],
  model_mapping: '',
  priority: 0,
  weight: 0,
  test_model: '',
  auto_ban: 1,
  fallback_sequence: '',
  status: CHANNEL_STATUS.ENABLED,
  status_code_mapping: '',
  tag: '',
  remark: '',
  setting: '',
  param_override: '',
  header_override: '',
  settings: '{}',
  other: '',
  multi_key_mode: 'single',
  multi_key_type: 'random',
  batch_add_set_key_prefix_2_name: false,
  key_mode: 'append',
  // Channel extra settings
  force_format: false,
  thinking_to_content: false,
  proxy: '',
  http_protocol: HTTP_PROTOCOL_AUTO,
  http2_connection_shards: 1,
  pass_through_body_enabled: false,
  gzip_enabled: false,
  system_prompt: '',
  system_prompt_override: false,
  // Type-specific settings
  channel_concurrency_limit: 0,
  channel_retry_times: 0,
  fallback_retry_enabled: false,
  channel_tpm_limit: 0,
  channel_tpm_buffer: 500_000,
  channel_limit_mode: 'reject',
  rewrite_upstream_429: false,
  is_enterprise_account: false,
  vertex_key_type: 'json',
  aws_key_type: 'ak_sk',
  azure_responses_version: '',
  // Field passthrough controls
  allow_service_tier: false,
  disable_store: false,
  allow_safety_identifier: false,
  allow_include_obfuscation: false,
  allow_inference_geo: false,
  allow_speed: false,
  claude_beta_query: false,
  disable_task_polling_sleep: false,
  station_cache_enabled: false,
  simulated_cache_creation_enabled: false,
  fake_non_stream_enabled: false,
  video_frame_extraction_enabled: false,
  disable_protocol_conversion: false,
  upstream_model_update_check_enabled: false,
  upstream_model_update_auto_sync_enabled: false,
  upstream_model_update_ignored_models: '',
  speed_protection_enabled: false,
  speed_protection_groups: [],
  advanced_custom: '',
}

// ============================================================================
// Transform Functions
// ============================================================================

/**
 * Transform Channel from API to Form default values
 */
export function transformChannelToFormDefaults(
  channel: Channel
): ChannelFormValues {
  // Parse channel extra settings from setting field
  let extraSettings = {
    force_format: false,
    thinking_to_content: false,
    proxy: '',
    http_protocol: HTTP_PROTOCOL_AUTO as 'auto' | 'http1',
    http2_connection_shards: 1,
    pass_through_body_enabled: false,
    gzip_enabled: false,
    system_prompt: '',
    system_prompt_override: false,
  }

  if (channel.setting) {
    try {
      const parsed = JSON.parse(channel.setting)
      const protocol = normalizeHttpProtocol(parsed.http_protocol)
      const shards = normalizeHttp2ConnectionShards(
        parsed.http2_connection_shards
      )
      extraSettings = {
        force_format: parsed.force_format || false,
        thinking_to_content: parsed.thinking_to_content || false,
        proxy: parsed.proxy || '',
        http_protocol: protocol,
        http2_connection_shards: protocol === HTTP_PROTOCOL_HTTP1 ? 1 : shards,
        pass_through_body_enabled: parsed.pass_through_body_enabled || false,
        gzip_enabled: parsed.gzip_enabled || false,
        system_prompt: parsed.system_prompt || '',
        system_prompt_override: parsed.system_prompt_override || false,
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel setting:', error)
    }
  }

  // Parse type-specific settings from settings field
  let vertexKeyType: 'json' | 'api_key' = 'json'
  let azureResponsesVersion = ''
  let isEnterpriseAccount = false
  let awsKeyType: 'ak_sk' | 'api_key' = 'ak_sk'
  let channelConcurrencyLimit = 0
  let channelTPMLimit = 0
  let channelTPMBuffer = 500_000
  let channelLimitMode: 'reject' | 'queue' = 'reject'
  let rewriteUpstream429 = false
  let allowServiceTier = false
  let disableStore = false
  let allowSafetyIdentifier = false
  let allowIncludeObfuscation = false
  let allowInferenceGeo = false
  let allowSpeed = false
  let claudeBetaQuery = false
  let disableTaskPollingSleep = false
  let stationCacheEnabled = false
  let simulatedCacheCreationEnabled = false
  let fakeNonStreamEnabled = false
  let videoFrameExtractionEnabled = false
  let disableProtocolConversion = false
  let upstreamModelUpdateCheckEnabled = false
  let upstreamModelUpdateAutoSyncEnabled = false
  let upstreamModelUpdateIgnoredModels = ''
  let speedProtectionEnabled = false
  let speedProtectionGroups: string[] = []
  let channelRetryTimes = 0
  let fallbackRetryEnabled = false
  let advancedCustom = ''

  if (channel.settings) {
    try {
      const parsed = JSON.parse(channel.settings)
      vertexKeyType = parsed.vertex_key_type || 'json'
      azureResponsesVersion = parsed.azure_responses_version || ''
      isEnterpriseAccount = parsed.openrouter_enterprise === true
      awsKeyType = parsed.aws_key_type || 'ak_sk'
      const parsedConcurrencyLimit =
        parsed.channel_concurrency_limit ?? parsed.channel_rpm_limit
      channelConcurrencyLimit =
        typeof parsedConcurrencyLimit === 'number' &&
        Number.isInteger(parsedConcurrencyLimit) &&
        parsedConcurrencyLimit >= 0 &&
        parsedConcurrencyLimit <= 10000
          ? parsedConcurrencyLimit
          : 0
      const parsedRetryTimes = parsed.channel_retry_times
      channelRetryTimes =
        typeof parsedRetryTimes === 'number' &&
        Number.isInteger(parsedRetryTimes) &&
        parsedRetryTimes >= 0 &&
        parsedRetryTimes <= 5
          ? parsedRetryTimes
          : 0
      fallbackRetryEnabled = parsed.fallback_retry_enabled === true
      const parsedTPMLimit = parsed.channel_tpm_limit
      channelTPMLimit =
        typeof parsedTPMLimit === 'number' &&
        Number.isInteger(parsedTPMLimit) &&
        parsedTPMLimit >= 0 &&
        parsedTPMLimit <= 100_000_000
          ? parsedTPMLimit
          : 0
      const parsedTPMBuffer = parsed.channel_tpm_buffer
      channelTPMBuffer =
        typeof parsedTPMBuffer === 'number' &&
        Number.isInteger(parsedTPMBuffer) &&
        parsedTPMBuffer >= 0 &&
        parsedTPMBuffer <= 100_000_000
          ? parsedTPMBuffer
          : 500_000
      channelLimitMode =
        parsed.channel_limit_mode === 'queue' ? 'queue' : 'reject'
      rewriteUpstream429 = parsed.rewrite_upstream_429 === true
      allowServiceTier = parsed.allow_service_tier === true
      disableStore = parsed.disable_store === true
      allowSafetyIdentifier = parsed.allow_safety_identifier === true
      allowIncludeObfuscation = parsed.allow_include_obfuscation === true
      allowInferenceGeo = parsed.allow_inference_geo === true
      allowSpeed = parsed.allow_speed === true
      claudeBetaQuery = parsed.claude_beta_query === true
      disableTaskPollingSleep = parsed.disable_task_polling_sleep === true
      stationCacheEnabled = parsed.station_cache_enabled === true
      simulatedCacheCreationEnabled =
        parsed.simulated_cache_creation_enabled === true
      fakeNonStreamEnabled = parsed.fake_non_stream_enabled === true
      videoFrameExtractionEnabled =
        parsed.video_frame_extraction_enabled === true
      disableProtocolConversion = parsed.disable_protocol_conversion === true
      upstreamModelUpdateCheckEnabled =
        parsed.upstream_model_update_check_enabled === true
      upstreamModelUpdateAutoSyncEnabled =
        parsed.upstream_model_update_auto_sync_enabled === true
      upstreamModelUpdateIgnoredModels = Array.isArray(
        parsed.upstream_model_update_ignored_models
      )
        ? parsed.upstream_model_update_ignored_models.join(',')
        : ''
      speedProtectionEnabled = parsed.speed_protection_enabled === true
      speedProtectionGroups = Array.isArray(parsed.speed_protection_groups)
        ? parsed.speed_protection_groups.filter(
            (group: unknown): group is string => typeof group === 'string'
          )
        : []
      if (parsed.advanced_custom) {
        advancedCustom = stringifyAdvancedCustomConfig(parsed.advanced_custom)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse channel settings:', error)
    }
  }

  return {
    name: channel.name || '',
    type: channel.type,
    base_url: channel.base_url || '',
    key: '', // Never populate key from backend for security
    openai_organization: channel.openai_organization || '',
    models: isFixedCodeChannelType(channel.type)
      ? fixedCodeChannelModels(
          channel.type,
          channel.models || '',
          channel.model_mapping || ''
        )
      : channel.models || '',
    test_model: isFixedCodeChannelType(channel.type)
      ? channel.test_model || getFixedCodeChannelModel(channel.type)
      : channel.test_model || '',
    group: parseGroups(channel.group || 'default'),
    model_mapping: channel.model_mapping || '',
    priority: channel.priority || 0,
    weight: channel.weight || 0,
    auto_ban: channel.auto_ban ?? 1,
    status: channel.status,
    fallback_sequence: channel.fallback_sequence || '',
    status_code_mapping: channel.status_code_mapping || '',
    tag: channel.tag || '',
    remark: channel.remark || '',
    setting: channel.setting || '',
    param_override: channel.param_override || '',
    header_override: channel.header_override || '',
    settings: channel.settings || '{}',
    other: channel.other || '',
    multi_key_mode: 'single',
    multi_key_type: channel.channel_info.multi_key_mode || 'random',
    batch_add_set_key_prefix_2_name: false,
    key_mode: 'append', // Default to append mode for editing multi-key channels
    // Channel extra settings
    ...extraSettings,
    // Type-specific settings
    channel_concurrency_limit: channelConcurrencyLimit,
    channel_retry_times: channelRetryTimes,
    fallback_retry_enabled: fallbackRetryEnabled,
    channel_tpm_limit: channelTPMLimit,
    channel_tpm_buffer: channelTPMBuffer,
    channel_limit_mode: channelLimitMode,
    rewrite_upstream_429: rewriteUpstream429,
    is_enterprise_account: isEnterpriseAccount,
    vertex_key_type: vertexKeyType,
    azure_responses_version: azureResponsesVersion,
    aws_key_type: awsKeyType,
    allow_service_tier: allowServiceTier,
    disable_store: disableStore,
    allow_include_obfuscation: allowIncludeObfuscation,
    allow_inference_geo: allowInferenceGeo,
    allow_speed: allowSpeed,
    claude_beta_query: claudeBetaQuery,
    disable_task_polling_sleep: disableTaskPollingSleep,
    station_cache_enabled: stationCacheEnabled,
    simulated_cache_creation_enabled: simulatedCacheCreationEnabled,
    fake_non_stream_enabled: fakeNonStreamEnabled,
    video_frame_extraction_enabled: videoFrameExtractionEnabled,
    disable_protocol_conversion: disableProtocolConversion,
    allow_safety_identifier: allowSafetyIdentifier,
    upstream_model_update_check_enabled: upstreamModelUpdateCheckEnabled,
    upstream_model_update_auto_sync_enabled: upstreamModelUpdateAutoSyncEnabled,
    upstream_model_update_ignored_models: upstreamModelUpdateIgnoredModels,
    speed_protection_enabled: speedProtectionEnabled,
    speed_protection_groups: speedProtectionGroups,
    advanced_custom: advancedCustom,
  }
}

/**
 * Build the setting JSON string from form extra settings
 */
export function buildSettingJSON(formData: ChannelFormValues): string {
  const settingObj: Record<string, unknown> = {
    force_format: formData.force_format || false,
    thinking_to_content: formData.thinking_to_content || false,
    proxy: formData.proxy?.trim() || '',
    pass_through_body_enabled: formData.pass_through_body_enabled || false,
    gzip_enabled: formData.gzip_enabled || false,
    system_prompt: formData.system_prompt || '',
    system_prompt_override: formData.system_prompt_override || false,
  }

  const protocol = normalizeHttpProtocol(formData.http_protocol)
  const shards =
    protocol === HTTP_PROTOCOL_HTTP1
      ? 1
      : normalizeHttp2ConnectionShards(formData.http2_connection_shards)

  // Omit defaults so unchanged channels keep equivalent JSON.
  if (protocol === HTTP_PROTOCOL_HTTP1) {
    settingObj.http_protocol = HTTP_PROTOCOL_HTTP1
  } else if (shards > 1) {
    settingObj.http2_connection_shards = shards
  }

  return JSON.stringify(settingObj)
}

/**
 * Build the settings JSON string (for type-specific config like vertex_key_type)
 */
function buildSettingsJSON(formData: ChannelFormValues): string {
  let settingsObj: Record<string, unknown> = {}

  // Try to parse existing settings first
  if (formData.settings && formData.settings !== '{}') {
    try {
      settingsObj = JSON.parse(formData.settings)
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to parse existing settings:', error)
    }
  }

  // Add vertex_key_type for Vertex AI channels (type 41)
  if (formData.type === 41) {
    settingsObj.vertex_key_type = formData.vertex_key_type || 'json'
  } else if ('vertex_key_type' in settingsObj) {
    delete settingsObj.vertex_key_type
  }

  // Add azure_responses_version for Azure channels (type 3)
  if (formData.type === 3 && formData.azure_responses_version) {
    settingsObj.azure_responses_version = formData.azure_responses_version
  } else if ('azure_responses_version' in settingsObj) {
    delete settingsObj.azure_responses_version
  }

  // Add enterprise account setting for OpenRouter (type 20)
  if (formData.type === 20) {
    settingsObj.openrouter_enterprise = formData.is_enterprise_account === true
  } else if ('openrouter_enterprise' in settingsObj) {
    delete settingsObj.openrouter_enterprise
  }

  // Add aws_key_type for AWS channels (type 33)
  if (formData.type === 33) {
    settingsObj.aws_key_type = formData.aws_key_type || 'ak_sk'
  } else if ('aws_key_type' in settingsObj) {
    delete settingsObj.aws_key_type
  }

  // Field passthrough controls:
  // - OpenAI, Anthropic, Codex, and New API: allow_service_tier
  // - OpenAI request fields: OpenAI, Codex, and New API
  // - Claude request fields: Anthropic and New API
  if (FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.allow_service_tier = formData.allow_service_tier === true
  } else if ('allow_service_tier' in settingsObj) {
    delete settingsObj.allow_service_tier
  }

  if (OPENAI_FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.disable_store = formData.disable_store === true
    settingsObj.allow_safety_identifier =
      formData.allow_safety_identifier === true
    settingsObj.allow_include_obfuscation =
      formData.allow_include_obfuscation === true
  } else {
    if ('disable_store' in settingsObj) {
      delete settingsObj.disable_store
    }
    if ('allow_safety_identifier' in settingsObj) {
      delete settingsObj.allow_safety_identifier
    }
    if ('allow_include_obfuscation' in settingsObj) {
      delete settingsObj.allow_include_obfuscation
    }
  }

  if (
    OPENAI_FIELD_PASSTHROUGH_TYPES.has(formData.type) ||
    CLAUDE_FIELD_PASSTHROUGH_TYPES.has(formData.type)
  ) {
    settingsObj.allow_inference_geo = formData.allow_inference_geo === true
  } else if ('allow_inference_geo' in settingsObj) {
    delete settingsObj.allow_inference_geo
  }

  if (CLAUDE_FIELD_PASSTHROUGH_TYPES.has(formData.type)) {
    settingsObj.allow_speed = formData.allow_speed === true
  } else if ('allow_speed' in settingsObj) {
    delete settingsObj.allow_speed
  }

  // Only the Anthropic adaptor supports forcing the Claude beta query.
  if (formData.type === 14) {
    settingsObj.claude_beta_query = formData.claude_beta_query === true
  } else if ('claude_beta_query' in settingsObj) {
    delete settingsObj.claude_beta_query
  }

  settingsObj.disable_task_polling_sleep =
    formData.disable_task_polling_sleep === true

  settingsObj.station_cache_enabled = formData.station_cache_enabled === true
  settingsObj.simulated_cache_creation_enabled =
    formData.simulated_cache_creation_enabled === true
  settingsObj.fake_non_stream_enabled =
    formData.fake_non_stream_enabled === true
  settingsObj.video_frame_extraction_enabled =
    formData.video_frame_extraction_enabled === true
  settingsObj.disable_protocol_conversion =
    formData.disable_protocol_conversion === true

  settingsObj.speed_protection_enabled =
    formData.speed_protection_enabled === true
  settingsObj.speed_protection_groups = [
    ...new Set(
      (formData.speed_protection_groups || [])
        .map((group) => group.trim())
        .filter(Boolean)
    ),
  ]

  if (isFixedCodeChannelType(formData.type)) {
    delete settingsObj.channel_concurrency_limit
    delete settingsObj.channel_rpm_limit
    delete settingsObj.channel_tpm_limit
    delete settingsObj.channel_tpm_buffer
    delete settingsObj.channel_limit_mode
    delete settingsObj.rewrite_upstream_429
  } else {
    delete settingsObj.channel_rpm_limit
    settingsObj.channel_concurrency_limit = Math.trunc(
      formData.channel_concurrency_limit || 0
    )
    settingsObj.channel_tpm_limit = Math.trunc(formData.channel_tpm_limit || 0)
    // The buffer only matters once a TPM limit exists; keep editing it anyway
    // so it is already in place when a limit is turned on later.
    settingsObj.channel_tpm_buffer = Math.trunc(
      formData.channel_tpm_buffer == null
        ? 500_000
        : formData.channel_tpm_buffer
    )
    settingsObj.channel_limit_mode =
      formData.channel_limit_mode === 'queue' ? 'queue' : 'reject'
    settingsObj.rewrite_upstream_429 = formData.rewrite_upstream_429 === true
  }
  const normalizedChannelRetryTimes = Math.min(
    5,
    Math.max(0, Math.trunc(formData.channel_retry_times || 0))
  )
  settingsObj.channel_retry_times = normalizedChannelRetryTimes
  settingsObj.fallback_retry_enabled =
    normalizedChannelRetryTimes > 0 && formData.fallback_retry_enabled === true

  // Upstream model update settings (for model-fetchable channel types)
  if (MODEL_FETCHABLE_TYPES.has(formData.type)) {
    settingsObj.upstream_model_update_check_enabled =
      formData.upstream_model_update_check_enabled === true
    settingsObj.upstream_model_update_auto_sync_enabled =
      settingsObj.upstream_model_update_check_enabled === true &&
      formData.upstream_model_update_auto_sync_enabled === true
    settingsObj.upstream_model_update_ignored_models = [
      ...new Set(
        String(formData.upstream_model_update_ignored_models || '')
          .split(',')
          .map((model) => model.trim())
          .filter(Boolean)
      ),
    ]
    if (
      !Array.isArray(settingsObj.upstream_model_update_last_detected_models) ||
      settingsObj.upstream_model_update_check_enabled !== true
    ) {
      settingsObj.upstream_model_update_last_detected_models = []
    }
    if (typeof settingsObj.upstream_model_update_last_check_time !== 'number') {
      settingsObj.upstream_model_update_last_check_time = 0
    }
  }

  if (formData.type === CHANNEL_TYPE_ADVANCED_CUSTOM) {
    const advancedCustomConfig = parseAdvancedCustomConfig(
      formData.advanced_custom
    )
    if (advancedCustomConfig) {
      settingsObj.advanced_custom = advancedCustomConfig
    }
  } else if ('advanced_custom' in settingsObj) {
    delete settingsObj.advanced_custom
  }

  return JSON.stringify(settingsObj)
}

function normalizeBaseUrl(value: string | undefined): string {
  return String(value || '')
    .trim()
    .replace(/\/+$/, '')
}

/**
 * Transform form data to API payload for creating channel
 */
export function transformFormDataToCreatePayload(formData: ChannelFormValues): {
  mode: 'single' | 'batch' | 'multi_to_single'
  multi_key_mode?: 'random' | 'polling'
  batch_add_set_key_prefix_2_name?: boolean
  channel: Partial<Channel>
} {
  const mode = formData.multi_key_mode || 'single'

  const channel: Partial<Channel> = {
    name: formData.name,
    type: formData.type,
    base_url: isFixedCodeChannelType(formData.type)
      ? fixedCodeChannelBaseUrl(formData.type)
      : normalizeBaseUrl(formData.base_url) || null,
    key: formData.key,
    openai_organization: formData.openai_organization || null,
    models: isFixedCodeChannelType(formData.type)
      ? fixedCodeChannelModels(
          formData.type,
          formData.models,
          formData.model_mapping
        )
      : formData.models,
    group: formatGroups(formData.group),
    model_mapping: formData.model_mapping || null,
    priority: formData.priority || null,
    weight: formData.weight || null,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    fallback_sequence: formData.fallback_sequence || null,
    status: formData.status,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Clean up empty strings to null for optional fields
  Object.keys(channel).forEach((key) => {
    if (channel[key as keyof typeof channel] === '') {
      ;(channel as Record<string, unknown>)[key] = null
    }
  })

  return {
    mode,
    multi_key_mode:
      mode === 'multi_to_single' ? formData.multi_key_type : undefined,
    batch_add_set_key_prefix_2_name:
      mode === 'batch' ? formData.batch_add_set_key_prefix_2_name : undefined,
    channel,
  }
}

/**
 * Transform form data to API payload for updating channel
 */
export function transformFormDataToUpdatePayload(
  formData: ChannelFormValues,
  channelId: number
): Partial<Channel> {
  const payload: Partial<Channel> = {
    id: channelId,
    name: formData.name,
    type: formData.type,
    base_url: isFixedCodeChannelType(formData.type)
      ? fixedCodeChannelBaseUrl(formData.type)
      : normalizeBaseUrl(formData.base_url) || null,
    openai_organization: formData.openai_organization || null,
    models: isFixedCodeChannelType(formData.type)
      ? fixedCodeChannelModels(
          formData.type,
          formData.models,
          formData.model_mapping
        )
      : formData.models,
    group: formatGroups(formData.group),
    priority: formData.priority || null,
    weight: formData.weight || null,
    test_model: formData.test_model || null,
    auto_ban: formData.auto_ban ?? 1,
    fallback_sequence: formData.fallback_sequence || null,
    status_code_mapping: formData.status_code_mapping || null,
    tag: formData.tag || null,
    remark: formData.remark || '',
    setting: buildSettingJSON(formData),
    param_override: formData.param_override || null,
    header_override: formData.header_override || null,
    settings: buildSettingsJSON(formData),
    other: formData.other || '',
  }

  // Only include key if it was changed (not empty)
  if (formData.key && formData.key.trim()) {
    payload.key = formData.key
  }

  // Clean up empty strings to null for optional fields
  Object.keys(payload).forEach((key) => {
    if (payload[key as keyof typeof payload] === '') {
      ;(payload as Record<string, unknown>)[key] = null
    }
  })

  // Send explicit empty strings for nullable fields so GORM updates can clear them.
  payload.base_url = isFixedCodeChannelType(formData.type)
    ? fixedCodeChannelBaseUrl(formData.type)
    : normalizeBaseUrl(formData.base_url) || ''
  payload.openai_organization = formData.openai_organization || ''
  payload.test_model = formData.test_model || ''
  payload.tag = formData.tag || ''
  payload.remark = formData.remark || ''
  payload.model_mapping = formData.model_mapping || ''
  payload.status_code_mapping = formData.status_code_mapping || ''
  payload.param_override = formData.param_override || ''
  payload.header_override = formData.header_override || ''
  payload.fallback_sequence = formData.fallback_sequence || ''

  return payload
}

// ============================================================================
// Validation Helpers
// ============================================================================

/**
 * Validate JSON string
 */
export function validateJSON(value: string): boolean {
  if (!value || value.trim() === '') return true
  try {
    JSON.parse(value)
    return true
  } catch {
    return false
  }
}

/**
 * Validate model mapping format
 */
export function validateModelMapping(value: string): boolean {
  if (!value || value.trim() === '') return true
  return validateJSON(value)
}

/**
 * Parse models string to array
 */
export function parseModels(models: string): string[] {
  if (!models) return []
  return models
    .split(',')
    .map((m) => m.trim())
    .filter((m) => m.length > 0)
}

/**
 * Parse groups string to array
 */
export function parseGroups(groups: string): string[] {
  if (!groups) return []
  return groups
    .split(',')
    .map((g) => g.trim())
    .filter((g) => g.length > 0)
}

/**
 * Format models array to string
 */
export function formatModels(models: string[]): string {
  return models.join(',')
}

/**
 * Format groups array to string
 */
export function formatGroups(groups: string[]): string {
  return groups.join(',')
}
