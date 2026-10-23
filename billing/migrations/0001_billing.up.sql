-- Invoices and their lines (docs/design.md); the numbers come from the SDK's document series (INV, per legal entity
-- and year, continuous). The order of drafting, issuing and formalizing is docs/flows/billing.md.

-- An invoice: one legal entity, one customer, one currency, one source order. Its ID is the claim ID at the source.
CREATE TABLE invoice (
    id              uuid PRIMARY KEY,
    number          text UNIQUE,
    legal_entity_id uuid NOT NULL,
    customer_id     uuid NOT NULL,
    customer_name   text NOT NULL,
    customer_tax_id text,
    currency        text NOT NULL,
    rate            numeric(19,10),
    rate_date       date,
    rate_type       text,
    source_kind     text NOT NULL DEFAULT 'sales_order' CHECK (source_kind IN ('sales_order')),
    source_id       uuid NOT NULL,
    state           text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'confirmed', 'issued', 'void')),
    formalization   text NOT NULL DEFAULT 'none' CHECK (formalization IN ('none', 'pending', 'done')),
    invoice_date    date,
    net             numeric(19,4) NOT NULL DEFAULT 0,
    tax             numeric(19,4) NOT NULL DEFAULT 0,
    -- the print snapshot, recorded at the first render: every later render uses the same version and view model
    print_template  text,
    print_version   bigint,
    print_data      text,
    print_digest    text,
    print_count     int NOT NULL DEFAULT 0,
    issued_at       timestamptz,
    search          text NOT NULL DEFAULT '',
    search_version  int NOT NULL DEFAULT 0,
    version         bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'issued') = (number IS NOT NULL))
);
CREATE INDEX invoice_list ON invoice (created_at DESC, id);
CREATE INDEX invoice_source ON invoice (source_id);

-- A line: what is billed of one source line; claimed is what the claim took (a draft lowers, never raises).
CREATE TABLE invoice_line (
    id             uuid PRIMARY KEY,
    invoice_id     uuid NOT NULL REFERENCES invoice (id),
    line_no        int NOT NULL,
    source_line_id uuid NOT NULL,
    product_id     uuid NOT NULL,
    product_code   text NOT NULL,
    product_name   text NOT NULL,
    unit           text NOT NULL,
    unit_price     numeric(19,6) NOT NULL,
    quantity       numeric(19,6) NOT NULL CHECK (quantity >= 0),
    net            numeric(19,4) NOT NULL CHECK (net >= 0),
    tax_rate       numeric(9,6) NOT NULL,
    tax            numeric(19,4) NOT NULL,
    claimed_qty    numeric(19,6) NOT NULL,
    claimed_amt    numeric(19,4) NOT NULL,
    UNIQUE (invoice_id, line_no),
    UNIQUE (invoice_id, source_line_id),
    CHECK (quantity <= claimed_qty AND net <= claimed_amt)
);

-- The invoice ID and the claimed lines of a draft request: a retry with the same key sends the same claim again, and
-- the source answers the first one (docs/flows/billing.md).
CREATE TABLE draft_key (
    principal  text NOT NULL,
    key        text NOT NULL,
    invoice_id uuid NOT NULL,
    lines      jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal, key)
);
