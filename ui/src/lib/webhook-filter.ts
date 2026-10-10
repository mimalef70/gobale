import type { EventDirection, Peer, PeerType, ProviderID, WebhookFilter } from './types'

export const peerTypes: PeerType[] = ['user', 'group', 'channel', 'bot', 'service']
export const eventDirections: EventDirection[] = ['incoming', 'outgoing', 'unknown']
export type FilterDraft = {
  peers: string
  exclude_peers: string
  sender_ids: string
  exclude_sender_ids: string
  peer_types: PeerType[]
  directions: EventDirection[]
}
export function filterDraft(filter: WebhookFilter = {}): FilterDraft {
  return {
    peers: (filter.peers ?? []).map((p) => `${p.type}:${p.id}`).join('\n'),
    exclude_peers: (filter.exclude_peers ?? []).map((p) => `${p.type}:${p.id}`).join('\n'),
    sender_ids: (filter.sender_ids ?? []).join('\n'),
    exclude_sender_ids: (filter.exclude_sender_ids ?? []).join('\n'),
    peer_types: filter.peer_types ?? [],
    directions: filter.directions ?? [],
  }
}
function canonicalID(value: string, provider: ProviderID) {
  if (provider === 'bale') return /^[1-9][0-9]{0,9}$/.test(value) && BigInt(value) <= 4294967295n
  if (provider === 'eitaa')
    return /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= 9223372036854775807n
  return (
    value.length > 0 &&
    new TextEncoder().encode(value).length <= 256 &&
    !/[\p{Cc}]/u.test(value) &&
    value.trim() === value
  )
}
function lines(value: string, valid: (line: string) => boolean) {
  const list = value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
  if (list.length > 100 || new Set(list).size !== list.length || !list.every(valid))
    throw new Error('INVALID_WEBHOOK_FILTER')
  return list
}
function peerID(type: string, value: string, provider: ProviderID) {
  if (provider === 'eitaa' && type === 'group' && value.startsWith('channel_'))
    return canonicalID(value.slice('channel_'.length), provider)
  if (provider !== 'rubika' && (type === 'bot' || type === 'service')) return false
 if (provider === 'rubika') {
 const prefix: Record<string, string> = { user: 'u', group: 'g', channel: 'c', bot: 'b', service: 's' }
 return new RegExp(`^${prefix[type] ?? '!'}0[A-Za-z0-9]{1,126}$`).test(value)
 }
 return canonicalID(value, provider)
}
function peers(value: string, provider: ProviderID): Peer[] {
  return lines(value, (line) => {
    const [type, id, extra] = line.split(':')
    return (
      extra === undefined &&
      peerTypes.includes(type as PeerType) &&
      peerID(type, id ?? '', provider)
    )
  }).map((line) => {
    const [type, id] = line.split(':')
    return { type: type as PeerType, id }
  })
}
export function parseFilterDraft(draft: FilterDraft, provider: ProviderID = 'bale'): WebhookFilter {
  if (provider !== 'rubika' && draft.peer_types.some((t) => t === 'bot' || t === 'service')) throw new Error('INVALID_WEBHOOK_FILTER')
 const filter: WebhookFilter = {}
  const included = peers(draft.peers, provider),
    excluded = peers(draft.exclude_peers, provider)
  const senders = lines(draft.sender_ids, (id) => provider === 'rubika' ? /^[ubs]0[A-Za-z0-9]{1,126}$/.test(id) : canonicalID(id, provider)),
    excludedSenders = lines(draft.exclude_sender_ids, (id) => provider === 'rubika' ? /^[ubs]0[A-Za-z0-9]{1,126}$/.test(id) : canonicalID(id, provider))
  if (included.length) filter.peers = included
  if (excluded.length) filter.exclude_peers = excluded
  if (senders.length) filter.sender_ids = senders
  if (excludedSenders.length) filter.exclude_sender_ids = excludedSenders
  if (draft.peer_types.length) filter.peer_types = [...draft.peer_types]
  if (draft.directions.length) filter.directions = [...draft.directions]
  return filter
}

// Filter fields are sets. Reordering inputs must not change effective routing.
export function filterKey(filter: WebhookFilter = {}) {
  return JSON.stringify({
    peers: (filter.peers ?? []).map((p) => `${p.type}:${p.id}`).sort(),
    exclude_peers: (filter.exclude_peers ?? []).map((p) => `${p.type}:${p.id}`).sort(),
    sender_ids: [...(filter.sender_ids ?? [])].sort(),
    exclude_sender_ids: [...(filter.exclude_sender_ids ?? [])].sort(),
    peer_types: [...(filter.peer_types ?? [])].sort(),
    directions: [...(filter.directions ?? [])].sort(),
  })
}
