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
