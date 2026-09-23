import { apiGet, apiSend } from './client'

export interface BankAccount {
  id: number
  fio_account_id: string
  iban: string | null
  currency: string
  display_name: string
  created_at: string
  has_token: boolean
  is_active: boolean
  /** null until the account's first successful sync */
  balance: string | null
  /** null until the account's first successful sync */
  balance_as_of: string | null
}

export interface CreateBankAccountPayload {
  fio_account_id: string
  display_name: string
  fio_token: string
  iban?: string
  currency?: string
}

// fio_token omitted (or undefined) leaves the existing token untouched —
// only send it on a deliberate token rotation.
export interface UpdateBankAccountPayload {
  display_name: string
  fio_token?: string
}

export function fetchBankAccounts(): Promise<BankAccount[]> {
  return apiGet<BankAccount[]>('/api/account')
}

export function createBankAccount(payload: CreateBankAccountPayload): Promise<BankAccount> {
  return apiSend<BankAccount>('POST', '/api/account', payload)
}

export function updateBankAccount(id: number, payload: UpdateBankAccountPayload): Promise<BankAccount> {
  return apiSend<BankAccount>('PATCH', `/api/account/${id}`, payload)
}

export function deleteBankAccount(id: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/account/${id}`)
}

// Both triggerFioSync and backfillAccount now run transaction
// processing/matching synchronously right after a successful fetch (see
// internal/handler/account.go), so both return the same shape.
export interface SyncResult {
  bank_account_id: number
  transactions_fetched: number
  transactions_inserted: number
  transactions_processed: number
  transactions_failed: number
}

// Manual "sync now" trigger, on top of the daily scheduled sync. Refused by
// the backend (409) in debug mode (DEBUG=true).
export function triggerFioSync(id: number): Promise<SyncResult> {
  return apiSend<SyncResult>('POST', `/api/account/${id}/sync`)
}

// One-off historical pull for a date range (YYYY-MM-DD, inclusive), for
// transactions predating an account's first cursor-based sync — data older
// than 90 days needs a manual strong-authorization (SCA) unlock done first in
// Fio's own Internet Banking. Refused by
// the backend (409) in debug mode (DEBUG=true).
export function backfillAccount(id: number, from: string, to: string): Promise<SyncResult> {
  return apiSend<SyncResult>('POST', `/api/account/${id}/backfill`, { from, to })
}

// One endpoint for every on-demand processing action, dispatched on `action`
// — see internal/handler/processing.go. `succeeded`/`failed` mean whatever
// that action's own unit of work is; new actions reuse this same shape
// rather than getting a bespoke response type each.
type ProcessingAction = 'run' | 'reclassify_internal_transfers'

interface ProcessingActionResult {
  action: ProcessingAction
  succeeded: number
  failed: number
}

function runProcessingAction(action: ProcessingAction): Promise<ProcessingActionResult> {
  return apiSend<ProcessingActionResult>('POST', '/api/processing/run', { action })
}

export interface ProcessingResult {
  transactions_processed: number
  transactions_failed: number
}

// Manual "run processing" trigger — not scoped to one account, re-runs
// matching for every raw_transactions row that doesn't have a
// processed_transactions row yet. Unlike sync/backfill, works fine in debug
// mode too (it never touches Fio, only already-stored raw_transactions).
// Needs manage-bank-accounts.
export async function triggerProcessing(): Promise<ProcessingResult> {
  const result = await runProcessingAction('run')
  return { transactions_processed: result.succeeded, transactions_failed: result.failed }
}

export interface ReclassifyResult {
  reclassified: number
  failed: number
}

// Re-checks already-processed transactions against the current bank_accounts
// roster and flips any now-recognizable transfer between our own accounts to
// internal_transfer — for a transfer synced/processed before its counterparty
// account was registered here (see internal/processing/processing.go). Skips
// manually-matched transactions; safe/idempotent to call repeatedly. Needs
// manage-transactions, not manage-bank-accounts, since it rewrites
// processed_transactions rows.
export async function reclassifyInternalTransfers(): Promise<ReclassifyResult> {
  const result = await runProcessingAction('reclassify_internal_transfers')
  return { reclassified: result.succeeded, failed: result.failed }
}
