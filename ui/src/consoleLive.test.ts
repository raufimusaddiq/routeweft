import assert from 'node:assert/strict'
import { test } from 'node:test'
import { entryMatches, mergeLive, parseConsoleLines, type Entry } from './consoleLive.ts'

const line = (id: number, message = `line ${id}`, level = 'INFO', attributes?: Record<string, unknown>): Entry => ({ id, time: '2026-10-30T00:00:00Z', level, message, ...(attributes ? { attributes } : {}) })

test('a console.log event is read into lines; anything malformed is dropped', () => {
  const read = parseConsoleLines({ lines: [line(1), { id: 'x' }, null, 7, { ...line(2), attributes: ['no'] }, { id: 3, time: 't', level: 'WARN', message: 'm', attributes: { a: 1 } }], truncated: true })
  assert.deepEqual(read.lines.map(entry => entry.id), [1, 2, 3])
  assert.equal(read.truncated, true)
  assert.equal(read.lines[1].attributes, undefined, 'attributes must be an object')
  assert.deepEqual(read.lines[2].attributes, { a: 1 })
  assert.deepEqual(parseConsoleLines(undefined), { lines: [], truncated: false })
  assert.deepEqual(parseConsoleLines({ lines: 'nope' }), { lines: [], truncated: false })
  assert.equal(parseConsoleLines({ lines: [], truncated: 'yes' }).truncated, false)
})

test('the live filter is the server\'s: level ignoring case, query in the message or the attributes', () => {
  const entry = line(1, 'Upstream Timed Out', 'WARN', { provider: 'Openai', status: 504 })
  assert.equal(entryMatches(entry, '', ''), true)
  assert.equal(entryMatches(entry, 'warn', ''), true)
  assert.equal(entryMatches(entry, 'ERROR', ''), false)
  assert.equal(entryMatches(entry, '', 'timed out'), true)
  assert.equal(entryMatches(entry, '', 'OPENAI'), true, 'an attribute value matches')
  assert.equal(entryMatches(entry, '', 'nothing'), false)
  assert.equal(entryMatches(line(2, 'plain'), '', 'x'), false)
  assert.equal(entryMatches(entry, ' warn ', '  timed  '.trim()), true)
})

test('new lines go on top, newest first, once each, within the page size', () => {
  const shown = [line(5), line(4), line(3)]
  const next = mergeLive(shown, [line(6), line(7)], '', '', 4)
  assert.deepEqual(next.items.map(entry => entry.id), [7, 6, 5, 4], 'bounded to the page size')
  assert.equal(next.added, 2)
  const again = mergeLive(next.items, [line(7), line(6)], '', '', 4)
  assert.equal(again.added, 0, 'a replayed batch adds nothing')
  assert.equal(again.items, next.items, 'and changes nothing')
})

test('lines that do not match the filter in force are not shown or counted', () => {
  const shown = [line(1, 'a', 'INFO')]
  const next = mergeLive(shown, [line(2, 'boom', 'ERROR'), line(3, 'fine', 'INFO')], 'ERROR', '', 10)
  assert.deepEqual(next.items.map(entry => entry.id), [2, 1])
  assert.equal(next.added, 1)
  assert.equal(mergeLive(shown, [line(4, 'x', 'INFO')], 'ERROR', '', 10).added, 0)
})
