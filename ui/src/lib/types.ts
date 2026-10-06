export type Scope = { id: string; instance_id: string }
export type ConnectionStatus = {
  auth: string
  transport: string
  recovery: string
  last_error?: string
}
export type RoutingRule = {
  source: 'device' | 'global'
  url: string
  events: string[]
  secret_configured: boolean
}
export type Webhook = {
  webhook_url: string
  webhook_events: string[] | null
  revision: number
  secret_configured?: boolean
  routing_mode?: 'none' | 'global' | 'device' | 'merged'
  routing_rules?: RoutingRule[]
}
export type Device = Scope & {
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
  sent_code_type?: number
  next_send_code_type?: number
  available_send_code_types?: number[]
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
  created_at: string
  payload?: unknown
}
export type Info = { name: string; version: string; release_stage: string; protocol_note: string }
export type WebhookPatch = {
  webhook_url?: string
  webhook_secret?: string
  webhook_events?: string[]
}
