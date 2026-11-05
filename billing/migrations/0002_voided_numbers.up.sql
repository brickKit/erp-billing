-- Invoice numbers voided without an invoice (be-protocol P14.5): a full recovery lost invoices issued after its
-- recovery point, raised the INV series past the numbers they took, and registers each skipped number here, so the
-- series stays accounted for (a tax authority asks for every number). The display form is INV-<legal entity code>-<period>-<number>.
CREATE TABLE voided_number (
    legal_entity_id uuid NOT NULL,
    series          text NOT NULL,
    period          text NOT NULL,
    number          bigint NOT NULL,
    reason          text NOT NULL,
    voided_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (legal_entity_id, series, period, number)
);
