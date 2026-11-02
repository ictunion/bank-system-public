-- +goose Up
-- +goose StatementBegin
-- The auditor's report for a budget year, a PDF produced after the year
-- closes. At most one per year — re-uploading replaces it. Stored in the DB
-- (a few MB, once a year) so it's covered by the same backup as everything
-- else.
CREATE TABLE budget_auditor_reports (
    budget_year INT PRIMARY KEY REFERENCES budgets(year) ON DELETE CASCADE,
    content     BYTEA NOT NULL,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE budget_auditor_reports;
-- +goose StatementEnd
