import { useEffect, useRef, useState } from 'react'

// useDebouncedCommit backs every filter field that can fire many change
// events in quick succession (typing in search/member #, or scrolling a date
// input's month/day/year spinner segment — each tick is a genuine native
// change, there's no separate "still fiddling" signal to key off): the input
// shows every change immediately (draft), but onCommit — the thing that
// actually updates the URL/query — only fires DEBOUNCE_MS after changes
// stop. If the committed value changes from elsewhere (Reset button, browser
// back/forward, or this same commit landing) the draft resyncs and any
// in-flight timer is cancelled, so a stale pending commit can't overwrite a
// reset that happened while the timer was still pending.
export function useDebouncedCommit(committed: string, onCommit: (value: string) => void, delayMs: number): [string, (value: string) => void] {
  const [draft, setDraft] = useState(committed)
  const timeoutRef = useRef<ReturnType<typeof setTimeout>>()

  useEffect(() => {
    setDraft(committed)
    clearTimeout(timeoutRef.current)
  }, [committed])

  useEffect(() => () => clearTimeout(timeoutRef.current), [])

  const handleChange = (value: string) => {
    setDraft(value)
    clearTimeout(timeoutRef.current)
    timeoutRef.current = setTimeout(() => onCommit(value), delayMs)
  }

  return [draft, handleChange]
}
