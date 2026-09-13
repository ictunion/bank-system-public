// Package fio parses responses from Fio Bank's classic "výpisy" export API
// (https://fioapi.fio.cz/v1/rest/...) — token-in-URL auth, flat per-transaction
// column JSON. Not to be confused with Fio's separate AISP v2 (PSD2) API, which
// uses a different auth scheme and a nested ISO 20022-style response shape.
package fio

import (
	"encoding/json"
	"fmt"
	"time"
)

// Column indices are Fio's own stable numbering for the classic export API.
const (
	colDate                  = "column0"
	colAmount                = "column1"
	colCounterAccountNumber  = "column2"
	colCounterBankCode       = "column3"
	colConstantSymbol        = "column4"
	colVariableSymbol        = "column5"
	colSpecificSymbol        = "column6"
	colUserIdentification    = "column7"
	colTransactionType       = "column8"
	colExecutor              = "column9"
	colCounterAccountName    = "column10"
	colCounterBankName       = "column12"
	colCurrency              = "column14"
	colMessageForRecipient   = "column16"
	colInstructionID         = "column17"
	colSpecification         = "column18"
	colFioTransactionID      = "column22"
	colComment               = "column25"
	colBIC                   = "column26"
)

// fioDateLayout matches Fio's "2024-01-15+0100" date format. The zone offset in
// the layout accepts either sign, so this also parses "-" offsets.
const fioDateLayout = "2006-01-02-0700"

type fioColumn struct {
	Value json.RawMessage `json:"value"`
	Name  string          `json:"name"`
	ID    int             `json:"id"`
}

type rawTransaction map[string]*fioColumn

// TransactionsResponse is the top-level shape returned by both the /last/ and
// /periods/ endpoints. TransactionList is the zero value (nil Transaction slice)
// when Fio has nothing new to report — not an error.
type TransactionsResponse struct {
	AccountStatement struct {
		Info struct {
			AccountID      string  `json:"accountId"`
			BankID         string  `json:"bankId"`
			Currency       string  `json:"currency"`
			IBAN           string  `json:"iban"`
			BIC            string  `json:"bic"`
			IDLastDownload int64   `json:"idLastDownload"`
		} `json:"info"`
		TransactionList struct {
			Transaction []rawTransaction `json:"transaction"`
		} `json:"transactionList"`
	} `json:"accountStatement"`
}

// Transaction is one parsed row, shaped to map directly onto raw_transactions columns.
type Transaction struct {
	FioTransactionID      int64
	TransactionDate       time.Time
	Amount                float64
	Currency              string
	CounterAccountNumber  *string
	CounterAccountName    *string
	CounterBankCode       *string
	CounterBankName       *string
	BIC                   *string
	VariableSymbol        *string
	SpecificSymbol        *string
	ConstantSymbol        *string
	UserIdentification    *string
	MessageForRecipient   *string
	TransactionType       *string
	Executor              *string
	Specification         *string
	Comment               *string
	InstructionID         *string
	RawPayload            json.RawMessage
}

// Transactions parses every raw transaction row in the response into typed
// Transaction values. Errors from a single malformed row are wrapped with its
// index rather than aborting the whole batch, since the caller still needs to
// know which rows in a 20+ transaction response failed.
func (r *TransactionsResponse) Transactions() ([]Transaction, error) {
	rows := r.AccountStatement.TransactionList.Transaction
	out := make([]Transaction, 0, len(rows))
	for i, row := range rows {
		tx, err := row.toTransaction()
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
		out = append(out, tx)
	}
	return out, nil
}

func (rt rawTransaction) toTransaction() (Transaction, error) {
	id, ok := rt.getInt64(colFioTransactionID)
	if !ok {
		return Transaction{}, fmt.Errorf("missing %s (fio_transaction_id)", colFioTransactionID)
	}

	dateStr := rt.getString(colDate)
	if dateStr == nil {
		return Transaction{}, fmt.Errorf("missing %s (date)", colDate)
	}
	parsedDate, err := time.Parse(fioDateLayout, *dateStr)
	if err != nil {
		return Transaction{}, fmt.Errorf("parsing date %q: %w", *dateStr, err)
	}
	// Keep only the calendar date Fio printed — the offset identifies which
	// wall-clock day this was in Prague, not an instant to convert further.
	date, err := time.Parse("2006-01-02", parsedDate.Format("2006-01-02"))
	if err != nil {
		return Transaction{}, err
	}

	amount, ok := rt.getFloat64(colAmount)
	if !ok {
		return Transaction{}, fmt.Errorf("missing %s (amount)", colAmount)
	}

	currency := rt.getString(colCurrency)
	if currency == nil {
		return Transaction{}, fmt.Errorf("missing %s (currency)", colCurrency)
	}

	payload, err := json.Marshal(rt)
	if err != nil {
		return Transaction{}, fmt.Errorf("marshaling raw payload: %w", err)
	}

	return Transaction{
		FioTransactionID:     id,
		TransactionDate:      date,
		Amount:               amount,
		Currency:             *currency,
		CounterAccountNumber: rt.getString(colCounterAccountNumber),
		CounterAccountName:   rt.getString(colCounterAccountName),
		CounterBankCode:      rt.getString(colCounterBankCode),
		CounterBankName:      rt.getString(colCounterBankName),
		BIC:                  rt.getString(colBIC),
		VariableSymbol:       rt.getString(colVariableSymbol),
		SpecificSymbol:       rt.getString(colSpecificSymbol),
		ConstantSymbol:       rt.getString(colConstantSymbol),
		UserIdentification:   rt.getString(colUserIdentification),
		MessageForRecipient:  rt.getString(colMessageForRecipient),
		TransactionType:      rt.getString(colTransactionType),
		Executor:             rt.getString(colExecutor),
		Specification:        rt.getString(colSpecification),
		Comment:              rt.getString(colComment),
		InstructionID:        rt.getString(colInstructionID),
		RawPayload:           payload,
	}, nil
}

func (rt rawTransaction) getString(col string) *string {
	c, ok := rt[col]
	if !ok || c == nil || isJSONNull(c.Value) {
		return nil
	}
	var s string
	if err := json.Unmarshal(c.Value, &s); err == nil {
		return &s
	}
	// Some columns Fio types as string (e.g. instruction ID) occasionally come
	// back as a bare JSON number — fall back to its literal text either way.
	raw := string(c.Value)
	return &raw
}

func (rt rawTransaction) getFloat64(col string) (float64, bool) {
	c, ok := rt[col]
	if !ok || c == nil || isJSONNull(c.Value) {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(c.Value, &f); err != nil {
		return 0, false
	}
	return f, true
}

func (rt rawTransaction) getInt64(col string) (int64, bool) {
	f, ok := rt.getFloat64(col)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}
