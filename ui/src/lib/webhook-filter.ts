import type { EventDirection, Peer, PeerType, WebhookFilter } from './types'

export const peerTypes: PeerType[] = ['user', 'group', 'channel']
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
function canonicalID(value: string) {
  return /^[1-9][0-9]{0,9}$/.test(value) && BigInt(value) <= 4294967295n
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
function peers(value: string): Peer[] {
  return lines(value, (line) => {
    const [type, id, extra] = line.split(':')
    return extra === undefined && peerTypes.includes(type as PeerType) && canonicalID(id ?? '')
  }).map((line) => {
    const [type, id] = line.split(':')
    return { type: type as PeerType, id }
  })
}
export function parseFilterDraft(draft: FilterDraft): WebhookFilter {
  const filter: WebhookFilter = {}
  const included = peers(draft.peers),
    excluded = peers(draft.exclude_peers)
  const senders = lines(draft.sender_ids, canonicalID),
    excludedSenders = lines(draft.exclude_sender_ids, canonicalID)
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
