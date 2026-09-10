package fio

import (
	"encoding/json"
	"testing"
)

func decodeResponse(t *testing.T, body string) *TransactionsResponse {
	t.Helper()
	var out TransactionsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return &out
}

func TestTransactions_ParsesAllFields(t *testing.T) {
	response := decodeResponse(t, `{
		"accountStatement": {
			"info": {"accountId": "123", "currency": "CZK"},
			"transactionList": {
				"transaction": [
					{
						"column0": {"value": "2026-06-15+0200", "name": "Date", "id": 0},
						"column1": {"value": 1234.56, "name": "Amount", "id": 1},
						"column2": {"value": "987654321", "name": "Counter account", "id": 2},
						"column3": {"value": "0800", "name": "Counter bank code", "id": 3},
						"column10": {"value": "Jane Doe", "name": "Counter account name", "id": 10},
						"column12": {"value": "Ceska sporitelna", "name": "Counter bank name", "id": 12},
						"column14": {"value": "CZK", "name": "Currency", "id": 14},
						"column5": {"value": "900601", "name": "VS", "id": 5},
						"column22": {"value": 9001, "name": "ID transakce", "id": 22},
						"column25": {"value": "membership dues", "name": "Comment", "id": 25}
					}
				]
			}
		}
	}`)

	transactions, err := response.Transactions()
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}

	got := transactions[0]
	if got.FioTransactionID != 9001 {
		t.Errorf("FioTransactionID = %d, want 9001", got.FioTransactionID)
	}
	if got.TransactionDate.Format("2006-01-02") != "2026-06-15" {
		t.Errorf("TransactionDate = %v, want 2026-06-15", got.TransactionDate)
	}
	if got.Amount != 1234.56 {
		t.Errorf("Amount = %v, want 1234.56", got.Amount)
	}
	if got.Currency != "CZK" {
		t.Errorf("Currency = %q, want CZK", got.Currency)
	}
	if got.CounterAccountNumber == nil || *got.CounterAccountNumber != "987654321" {
		t.Errorf("CounterAccountNumber = %v, want 987654321", got.CounterAccountNumber)
	}
	if got.CounterAccountName == nil || *got.CounterAccountName != "Jane Doe" {
		t.Errorf("CounterAccountName = %v, want Jane Doe", got.CounterAccountName)
	}
	if got.VariableSymbol == nil || *got.VariableSymbol != "900601" {
		t.Errorf("VariableSymbol = %v, want 900601", got.VariableSymbol)
	}
	if got.Comment == nil || *got.Comment != "membership dues" {
		t.Errorf("Comment = %v, want \"membership dues\"", got.Comment)
	}
}

func TestTransactions_EmptyListIsNilNotError(t *testing.T) {
	response := decodeResponse(t, `{"accountStatement": {"info": {}, "transactionList": {}}}`)

	transactions, err := response.Transactions()
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(transactions) != 0 {
		t.Errorf("got %d transactions, want 0", len(transactions))
	}
}

func TestTransactions_MissingRequiredColumnFails(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			"missing fio_transaction_id (column22)",
			`{"accountStatement":{"info":{},"transactionList":{"transaction":[
				{"column0":{"value":"2026-06-15+0200","name":"Date","id":0},"column1":{"value":10,"name":"Amount","id":1},"column14":{"value":"CZK","name":"Currency","id":14}}
			]}}}`,
		},
		{
			"missing date (column0)",
			`{"accountStatement":{"info":{},"transactionList":{"transaction":[
				{"column1":{"value":10,"name":"Amount","id":1},"column14":{"value":"CZK","name":"Currency","id":14},"column22":{"value":1,"name":"ID","id":22}}
			]}}}`,
		},
		{
			"malformed date",
			`{"accountStatement":{"info":{},"transactionList":{"transaction":[
				{"column0":{"value":"not-a-date","name":"Date","id":0},"column1":{"value":10,"name":"Amount","id":1},"column14":{"value":"CZK","name":"Currency","id":14},"column22":{"value":1,"name":"ID","id":22}}
			]}}}`,
		},
		{
			"missing amount (column1)",
			`{"accountStatement":{"info":{},"transactionList":{"transaction":[
				{"column0":{"value":"2026-06-15+0200","name":"Date","id":0},"column14":{"value":"CZK","name":"Currency","id":14},"column22":{"value":1,"name":"ID","id":22}}
			]}}}`,
		},
		{
			"missing currency (column14)",
			`{"accountStatement":{"info":{},"transactionList":{"transaction":[
				{"column0":{"value":"2026-06-15+0200","name":"Date","id":0},"column1":{"value":10,"name":"Amount","id":1},"column22":{"value":1,"name":"ID","id":22}}
			]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := decodeResponse(t, tt.body)
			if _, err := response.Transactions(); err == nil {
				t.Error("Transactions returned nil error, want a parse failure")
			}
		})
	}
}

func TestTransactions_NullColumnValueTreatedAsAbsent(t *testing.T) {
	response := decodeResponse(t, `{"accountStatement":{"info":{},"transactionList":{"transaction":[
		{"column0":{"value":"2026-06-15+0200","name":"Date","id":0},"column1":{"value":10,"name":"Amount","id":1},"column14":{"value":"CZK","name":"Currency","id":14},"column22":{"value":1,"name":"ID","id":22},"column5":{"value":null,"name":"VS","id":5}}
	]}}}`)

	transactions, err := response.Transactions()
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
	if transactions[0].VariableSymbol != nil {
		t.Errorf("VariableSymbol = %v, want nil for a JSON-null column value", transactions[0].VariableSymbol)
	}
}
