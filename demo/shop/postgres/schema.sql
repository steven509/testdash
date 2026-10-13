CREATE TABLE invoices (
  id          BIGSERIAL PRIMARY KEY,
  number      TEXT GENERATED ALWAYS AS ('INV-' || lpad(id::text, 6, '0')) STORED,
  order_id    BIGINT NOT NULL UNIQUE,
  customer_id TEXT NOT NULL,
  net         NUMERIC(10, 2) NOT NULL,
  vat         NUMERIC(10, 2) NOT NULL,
  total       NUMERIC(10, 2) NOT NULL,
  issued_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE invoice_lines (
  invoice_id BIGINT NOT NULL REFERENCES invoices (id),
  sku        TEXT NOT NULL,
  qty        INT NOT NULL,
  price      NUMERIC(10, 2) NOT NULL
);
