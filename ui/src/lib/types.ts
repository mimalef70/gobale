export type ProviderID = 'bale' | 'eitaa' | 'rubika'
export type ProviderSend = {
  kinds: Array<'text' | 'file' | 'image' | 'audio' | 'voice' | 'video'>
  max_text_bytes?: number
  max_text_characters?: number
  mentions_supported: boolean
  max_mentions: number
  reply_supported: boolean
  media_format_notes: { voice?: string; audio?: string; video?: string }
}
export type OperationPreset = { operation: string; parameters: Record<string, unknown> }
export type ProviderInteractions = {
  typing?: OperationPreset
  typing_stop?: OperationPreset
  online?: OperationPreset
  offline?: OperationPreset
  read_operation?: string
  read_argument?: 'date' | 'message_id'
  receipt_model: 'none' | 'timestamp_watermark' | 'message_id_watermark' | 'chat_state'
  read_events: boolean
  delivered_events: boolean
  sender_names: 'enriched' | 'unavailable'
  avatar_download: boolean
  partial_edits: boolean
}
export type Provider = {
  id: ProviderID
  name: string
  enabled: boolean
  verification: string
  delivery_methods: string[]
  send: ProviderSend
  interactions: ProviderInteractions
}
export type Scope = { id: string; instance_id: string }
export type PeerType = 'user' | 'group' | 'channel' | 'bot' | 'service'
export type EventDirection = 'incoming' | 'outgoing' | 'unknown'
export type Peer = { type: PeerType; id: string }
export type WebhookFilter = {
  peers?: Peer[]
  exclude_peers?: Peer[]
  peer_types?: PeerType[]
  sender_ids?: string[]
  exclude_sender_ids?: string[]
  directions?: EventDirection[]
}
export type ConnectionStatus = {
  auth: string
  transport: string
  recovery: string
  last_error?: string
 recovery_issue?: string
 unsupported_updates_observed?: boolean
}
export type RoutingRule = {
  source: 'device' | 'global'
  url: string
  events: string[]
  secret_configured: boolean
  filter?: WebhookFilter
}
export type Webhook = {
  webhook_url: string
  webhook_events: string[] | null
  webhook_filter?: WebhookFilter
  revision: number
  secret_configured?: boolean
  routing_mode?: 'none' | 'global' | 'device' | 'merged'
  routing_rules?: RoutingRule[]
}
export type Device = Scope & {
  provider: ProviderID
  account_id?: string
  created_at: string
  webhook: Webhook
  status: ConnectionStatus
  deliveries: { pending: number; failed: number; paused: number }
}
export type Overview = { server_time: string; devices: Device[] }
export type Challenge = {
  challenge_id: string
  state?: string
  expires_at: string
  resend_available_at: string
  masked_phone: string
  delivery?: string
  next_delivery?: string
  available_deliveries?: string[]
}
export type LoginState = { state: string; challenge: Challenge | null; server_time: string }
export type Session = { csrf_token: string; expires_at: string; absolute_expires_at: string }
export type Delivery = {
  delivery_id: string
  event_id: string
  device_id: string
  url: string
  state: string
  attempts: number
  next_attempt_at: string
  last_error?: string
 recovery_issue?: string
 unsupported_updates_observed?: boolean
  created_at: string
  payload?: unknown
}
export type Info = {
  name: string
  version: string
  release_stage: string
  protocol_note: string
  providers: Provider[]
  capabilities: {
    multi_device: boolean
    multi_provider: boolean
    per_device_webhook: boolean
    durable_outbox: boolean
    scheduled_sends: boolean
  }
}
export type WebhookPatch = {
  webhook_url?: string
  webhook_secret?: string
  webhook_events?: string[]
  webhook_filter?: WebhookFilter
}
