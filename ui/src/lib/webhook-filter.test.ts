import { expect, it } from 'vitest'
import { webhookPatch } from './api'
import { filterDraft, parseFilterDraft } from './webhook-filter'

it('keeps filter IDs as strings and supports an explicit empty object reset', () => {
  const draft = filterDraft()
  draft.peers = 'user:4294967295\ngroup:123'
  draft.sender_ids = '4294967295'
  const filter = parseFilterDraft(draft)
  expect(filter).toEqual({
    peers: [
      { type: 'user', id: '4294967295' },
      { type: 'group', id: '123' },
    ],
    sender_ids: ['4294967295'],
  })
  const original = { webhook_url: '', webhook_events: [], webhook_filter: filter }
  expect(webhookPatch(original, '', [], false, '')).toEqual({})
  expect(webhookPatch(original, '', [], false, '', filter)).toEqual({})
  expect(webhookPatch(original, '', [], false, '', {})).toEqual({ webhook_filter: {} })
})

it.each(['0', '01', '-1', '+1', '1.0', '1e2', '4294967296', '۱۲۳', '1\n1'])(
  'rejects invalid or duplicate sender IDs: %s',
  (sender_ids) => {
    expect(() => parseFilterDraft({ ...filterDraft(), sender_ids })).toThrow(
      'INVALID_WEBHOOK_FILTER',
    )
  },
)
it.each(['unknown:1', 'user:01', 'group:1:2', 'channel:0', 'user:1\nuser:1'])(
  'rejects invalid or duplicate peers: %s',
  (peers) => {
    expect(() => parseFilterDraft({ ...filterDraft(), peers })).toThrow('INVALID_WEBHOOK_FILTER')
  },
)
it('bounds every input list and ignores set ordering without mutating the source', () => {
  const draft = filterDraft()
  draft.sender_ids = Array.from({ length: 100 }, (_, i) => String(i + 1)).join('\n')
  expect(parseFilterDraft(draft).sender_ids).toHaveLength(100)
  draft.sender_ids += '\n101'
  expect(() => parseFilterDraft(draft)).toThrow('INVALID_WEBHOOK_FILTER')
  const original = {
    webhook_url: '',
    webhook_events: [],
    webhook_filter: { sender_ids: ['2', '1'] },
  }
  expect(webhookPatch(original, '', [], false, '', { sender_ids: ['1', '2'] })).toEqual({})
  expect(original.webhook_filter.sender_ids).toEqual(['2', '1'])
})

it('preserves opaque sender IDs for another provider without applying Bale uint32 rules', () => {
  const draft = { ...filterDraft(), peers: 'user:u0synthetic', sender_ids: 'u0synthetic' }
  expect(parseFilterDraft(draft, 'rubika')).toEqual({
    peers: [{ type: 'user', id: 'u0synthetic' }],
    sender_ids: ['u0synthetic'],
  })
  expect(() => parseFilterDraft(draft, 'bale')).toThrow('INVALID_WEBHOOK_FILTER')
  expect(() => parseFilterDraft({ ...draft, sender_ids: 'u0\u0000bad' }, 'rubika')).toThrow(
    'INVALID_WEBHOOK_FILTER',
  )
})

it('preserves distinct Eitaa classic-group and supergroup filter identities', () => {
  const filter = parseFilterDraft(
    {
      ...filterDraft(),
      peers: 'group:91\ngroup:channel_91\nchannel:91',
      exclude_peers: 'group:channel_9223372036854775807',
      sender_ids: '9223372036854775807',
    },
    'eitaa',
  )
  expect(filter.peers).toEqual([
    { type: 'group', id: '91' },
    { type: 'group', id: 'channel_91' },
    { type: 'channel', id: '91' },
  ])
  expect(parseFilterDraft(filterDraft(filter), 'eitaa')).toEqual(filter)
  expect(webhookPatch({ webhook_url: '', webhook_events: [] }, '', [], false, '', filter)).toEqual({
    webhook_filter: filter,
  })
})

it.each([
  'user:channel_91',
  'channel:channel_91',
  'group:channel_01',
  'group:channel_0',
  'group:channel_9223372036854775808',
  'group:9223372036854775808',
])('rejects an invalid Eitaa peer namespace or numeric bound: %s', (peers) => {
  expect(() => parseFilterDraft({ ...filterDraft(), peers }, 'eitaa')).toThrow(
    'INVALID_WEBHOOK_FILTER',
  )
})
it('does not accept an Eitaa supergroup identity as a sender ID', () => {
  expect(() => parseFilterDraft({ ...filterDraft(), sender_ids: 'channel_91' }, 'eitaa')).toThrow(
    'INVALID_WEBHOOK_FILTER',
  )
})

it('preserves Rubika bot and service namespaces and validates actors', () => {
  const draft = filterDraft({ peers: [{ type: 'bot', id: 'b0fixture' }, { type: 'service', id: 's0fixture' }], sender_ids: ['b0fixture', 's0fixture'], peer_types: ['bot', 'service'] })
  expect(parseFilterDraft(draft, 'rubika').peers).toEqual([{ type: 'bot', id: 'b0fixture' }, { type: 'service', id: 's0fixture' }])
  expect(() => parseFilterDraft(draft, 'bale')).toThrow('INVALID_WEBHOOK_FILTER')
  expect(() => parseFilterDraft(draft, 'eitaa')).toThrow('INVALID_WEBHOOK_FILTER')
  expect(() => parseFilterDraft({ ...draft, peers: 'user:s0fixture' }, 'rubika')).toThrow('INVALID_WEBHOOK_FILTER')
  expect(() => parseFilterDraft({ ...draft, sender_ids: 'g0fixture' }, 'rubika')).toThrow('INVALID_WEBHOOK_FILTER')
})
