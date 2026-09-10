import { apiGet } from './client'

export type EventType = 'fio_sync' | 'orca_sync'

export interface EventLogItem {
  event_type: EventType
  id: number
  started_at: string
  finished_at: string | null
  status: string
  detail: string | null
  fetched: number | null
  processed: number | null
  error_message: string | null
}

export interface EventLogsResponse {
  total: number
  limit: number
  offset: number
  events: EventLogItem[]
}

export function fetchEventLogs(limit: number, offset: number): Promise<EventLogsResponse> {
  return apiGet<EventLogsResponse>(`/api/event-logs?limit=${limit}&offset=${offset}`)
}
