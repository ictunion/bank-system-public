import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { errMessage } from './api/client'
import {
  type BankAccount,
  backfillAccount,
  createBankAccount,
  deleteBankAccount,
  fetchBankAccounts,
  reclassifyInternalTransfers,
  triggerFioSync,
  triggerProcessing,
  updateBankAccount,
} from './api/bankAccounts'
import { Overlay } from './Overlay'
import { useHasRole } from './roles'

const COLUMNS = ['Fio account', 'Name', 'Currency', 'Balance', 'IBAN', 'Token', 'Created', ''] as const

// Same approach as BudgetPage's formatAmount — Intl currency formatting with
// a plain-number fallback for a currency code Intl doesn't recognize.
function formatAmount(total: string, currency: string): string {
  const n = Number(total)
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(n)
  } catch {
    return `${n.toLocaleString()} ${currency}`
  }
}

export function BankAccountsPage() {
  const qc = useQueryClient()
  const canManageTransactions = useHasRole('manage-transactions')
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<BankAccount | null>(null)
  const [backfilling, setBackfilling] = useState<BankAccount | null>(null)

  const { data, isPending, isError, error } = useQuery({
    queryKey: ['bank-accounts'],
    queryFn: fetchBankAccounts,
  })

  // Not scoped to one account — re-runs matching for every raw_transactions
  // row that doesn't have a processed_transactions row yet (see
  // api/bankAccounts.ts triggerProcessing). Invalidate the transaction
  // browser's cache too so a subsequent visit shows freshly matched rows.
  const runProcessing = useMutation({
    mutationFn: triggerProcessing,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['transactions'] }),
  })

  // Separate from runProcessing: rewrites already-processed rows (a transfer
  // synced/processed before its counterparty account was registered here —
  // see api/bankAccounts.ts reclassifyInternalTransfers), so it needs
  // manage-transactions, not manage-bank-accounts, and gets its own explicit
  // button rather than running automatically.
  const reclassify = useMutation({
    mutationFn: reclassifyInternalTransfers,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['transactions'] }),
  })

  return (
    <section>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>Bank accounts</h2>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'baseline' }}>
          {runProcessing.error && <span style={{ color: '#b00' }}>{errMessage(runProcessing.error)}</span>}
          {runProcessing.data && (
            <span style={{ color: '#080' }}>
              Processed {runProcessing.data.transactions_processed}
              {runProcessing.data.transactions_failed > 0 ? ` (${runProcessing.data.transactions_failed} failed)` : ''}
            </span>
          )}
          <button disabled={runProcessing.isPending} onClick={() => runProcessing.mutate()}>
            {runProcessing.isPending ? 'Running…' : 'Run processing'}
          </button>
          {canManageTransactions && (
            <>
              {reclassify.error && <span style={{ color: '#b00' }}>{errMessage(reclassify.error)}</span>}
              {reclassify.data && (
                <span style={{ color: '#080' }}>
                  Reclassified {reclassify.data.reclassified}
                  {reclassify.data.failed > 0 ? ` (${reclassify.data.failed} failed)` : ''}
                </span>
              )}
              <button
                disabled={reclassify.isPending}
                title="Re-check already-processed transactions against the current account list — fixes a transfer synced before its counterparty account was added"
                onClick={() => reclassify.mutate()}
              >
                {reclassify.isPending ? 'Reclassifying…' : 'Reclassify internal transfers'}
              </button>
            </>
          )}
          <button onClick={() => setAdding(true)}>Add bank account</button>
        </div>
      </div>

      {isPending ? (
        <p>Loading bank accounts…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
            <thead>
              <tr>
                {COLUMNS.map((h, i) => (
                  <th key={i} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.map((a) => (
                <BankAccountRow
                  key={a.id}
                  account={a}
                  onEdit={() => setEditing(a)}
                  onBackfill={() => setBackfilling(a)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && <AddBankAccountDialog onClose={() => setAdding(false)} />}
      {editing && <EditBankAccountDialog account={editing} onClose={() => setEditing(null)} />}
      {backfilling && <BackfillDialog account={backfilling} onClose={() => setBackfilling(null)} />}
    </section>
  )
}

function BankAccountRow({
  account,
  onEdit,
  onBackfill,
}: {
  account: BankAccount
  onEdit: () => void
  onBackfill: () => void
}) {
  const qc = useQueryClient()
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [syncMessage, setSyncMessage] = useState<{ text: string; isError: boolean } | null>(null)

  const remove = useMutation({
    mutationFn: () => deleteBankAccount(account.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['bank-accounts'] }),
    onError: (err) => setDeleteError(errMessage(err)),
  })

  const sync = useMutation({
    mutationFn: () => triggerFioSync(account.id),
    onSuccess: (result) =>
      setSyncMessage({
        text: `Synced: ${result.transactions_fetched} fetched, ${result.transactions_inserted} new, ${result.transactions_processed} processed`,
        isError: false,
      }),
    onError: (err) => setSyncMessage({ text: errMessage(err), isError: true }),
  })

  // Soft-deleted accounts stay in the list as a historical record (crossed
  // out) rather than disappearing — see api/bankAccounts.ts is_active.
  const cell: CSSProperties = account.is_active ? td : { ...td, textDecoration: 'line-through', color: '#999' }

  return (
    <tr>
      <td style={cell}>{account.fio_account_id}</td>
      <td style={cell}>{account.display_name}</td>
      <td style={cell}>{account.currency}</td>
      <td style={cell} title={account.balance_as_of ? `as of ${new Date(account.balance_as_of).toLocaleString()}` : undefined}>
        {account.balance == null ? '—' : formatAmount(account.balance, account.currency)}
      </td>
      <td style={cell}>{account.iban ?? '—'}</td>
      <td style={cell}>{account.has_token ? 'Set' : 'Not set'}</td>
      <td style={cell}>{account.created_at.slice(0, 10)}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        {account.is_active ? (
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <button onClick={onEdit}>Edit</button>
            <button
              disabled={remove.isPending}
              onClick={() => {
                setDeleteError(null)
                if (window.confirm(`Delete bank account "${account.display_name}"?`)) {
                  remove.mutate()
                }
              }}
            >
              {remove.isPending ? 'Deleting…' : 'Delete'}
            </button>
            <button
              disabled={sync.isPending || !account.has_token}
              title={account.has_token ? undefined : 'No Fio token configured'}
              onClick={() => {
                setSyncMessage(null)
                sync.mutate()
              }}
            >
              {sync.isPending ? 'Syncing…' : 'Sync now'}
            </button>
            <button
              disabled={!account.has_token}
              title={
                account.has_token
                  ? 'Pull a historical date range — for transactions predating this account’s first daily sync'
                  : 'No Fio token configured'
              }
              onClick={onBackfill}
            >
              Backfill…
            </button>
            {deleteError && <span style={{ color: '#b00' }}>{deleteError}</span>}
            {syncMessage && (
              <span style={{ color: syncMessage.isError ? '#b00' : '#080' }}>{syncMessage.text}</span>
            )}
          </div>
        ) : (
          <span style={{ color: '#999' }}>Deleted</span>
        )}
      </td>
    </tr>
  )
}

// Backfills a historical date range via Fio's /periods/ endpoint (never
// touches the daily sync's cursor — see api/bankAccounts.ts). Meant for the
// gap before an account's first cursor-based sync: data older than 90 days
// needs a manual SCA unlock in Fio's own Internet Banking first, or the
// backend call below fails with that explained in the error message.
function BackfillDialog({ account, onClose }: { account: BankAccount; onClose: () => void }) {
  const today = new Date().toISOString().slice(0, 10)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState(today)

  const backfill = useMutation({
    mutationFn: () => backfillAccount(account.id, from, to),
  })

  const canRun = from !== '' && to !== '' && !backfill.isPending

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Backfill {account.display_name}</h2>
      <p style={{ color: '#666', fontSize: '0.875rem', marginTop: 0, maxWidth: 420 }}>
        Pulls transactions in this date range from Fio, independent of the daily sync — safe to
        run any time, overlap with already-synced transactions is skipped automatically. Data
        older than 90 days needs a strong-authorization (SCA) unlock done first in Fio's own
        Internet Banking.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canRun) backfill.mutate()
        }}
      >
        <label style={field}>
          From
          <input type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} autoFocus />
        </label>

        <label style={field}>
          To
          <input type="date" value={to} min={from || undefined} max={today} onChange={(e) => setTo(e.target.value)} />
        </label>

        {backfill.error && <p style={{ color: '#b00' }}>{errMessage(backfill.error)}</p>}
        {backfill.data && (
          <p style={{ color: '#080' }}>
            Fetched {backfill.data.transactions_fetched}, inserted {backfill.data.transactions_inserted} new,
            processed {backfill.data.transactions_processed}
            {backfill.data.transactions_failed > 0 ? ` (${backfill.data.transactions_failed} failed)` : ''}.
          </p>
        )}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canRun}>
            {backfill.isPending ? 'Running…' : 'Run backfill'}
          </button>
          <button type="button" onClick={onClose}>
            {backfill.isSuccess ? 'Close' : 'Cancel'}
          </button>
        </div>
      </form>
    </Overlay>
  )
}

function AddBankAccountDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()

  const [fioAccountId, setFioAccountId] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [currency, setCurrency] = useState('CZK')
  const [iban, setIban] = useState('')
  const [fioToken, setFioToken] = useState('')

  const create = useMutation({
    mutationFn: () =>
      createBankAccount({
        fio_account_id: fioAccountId,
        display_name: displayName,
        currency,
        iban: iban || undefined,
        fio_token: fioToken,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['bank-accounts'] })
      onClose()
    },
  })

  const canSave =
    fioAccountId.trim() !== '' &&
    displayName.trim() !== '' &&
    currency.trim().length === 3 &&
    fioToken.trim() !== '' &&
    !create.isPending

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Add bank account</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) create.mutate()
        }}
      >
        <label style={field}>
          Fio account number
          <input
            value={fioAccountId}
            onChange={(e) => setFioAccountId(e.target.value)}
            autoFocus
          />
        </label>

        <label style={field}>
          Display name
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </label>

        <label style={field}>
          Currency
          <input
            value={currency}
            maxLength={3}
            style={{ width: '5rem', textTransform: 'uppercase' }}
            onChange={(e) => setCurrency(e.target.value.toUpperCase())}
          />
        </label>

        <label style={field}>
          IBAN (optional)
          <input value={iban} onChange={(e) => setIban(e.target.value)} />
        </label>

        <label style={field}>
          Fio API token
          <input
            type="password"
            autoComplete="new-password"
            value={fioToken}
            onChange={(e) => setFioToken(e.target.value)}
          />
        </label>

        {create.error && <p style={{ color: '#b00' }}>{errMessage(create.error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canSave}>
            {create.isPending ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Overlay>
  )
}

// fio_account_id/iban/currency aren't editable here — they're properties of
// the real Fio account, not our metadata. Only display_name and (optionally)
// a token rotation. Leaving the token field blank keeps the existing token.
function EditBankAccountDialog({ account, onClose }: { account: BankAccount; onClose: () => void }) {
  const qc = useQueryClient()

  const [displayName, setDisplayName] = useState(account.display_name)
  const [fioToken, setFioToken] = useState('')

  const update = useMutation({
    mutationFn: () =>
      updateBankAccount(account.id, {
        display_name: displayName,
        fio_token: fioToken.trim() || undefined,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['bank-accounts'] })
      onClose()
    },
  })

  const canSave = displayName.trim() !== '' && !update.isPending

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Edit bank account</h2>
      <p style={{ color: '#666', fontSize: '0.875rem', marginTop: 0 }}>
        {account.fio_account_id} · {account.currency}
        {account.iban ? ` · ${account.iban}` : ''}
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) update.mutate()
        }}
      >
        <label style={field}>
          Display name
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} autoFocus />
        </label>

        <label style={field}>
          New Fio API token (leave blank to keep current)
          <input
            type="password"
            autoComplete="new-password"
            value={fioToken}
            onChange={(e) => setFioToken(e.target.value)}
          />
        </label>

        {update.error && <p style={{ color: '#b00' }}>{errMessage(update.error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canSave}>
            {update.isPending ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Overlay>
  )
}

const field: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '0.25rem',
  marginBottom: '0.75rem',
  maxWidth: 320,
}

const th: CSSProperties = {
  textAlign: 'left',
  borderBottom: '2px solid #ccc',
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}

const td: CSSProperties = {
  borderBottom: '1px solid #eee',
  padding: '0.35rem 0.6rem',
  whiteSpace: 'nowrap',
}
