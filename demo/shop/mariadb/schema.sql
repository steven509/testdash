CREATE TABLE products (
  sku       VARCHAR(16) PRIMARY KEY,
  name      VARCHAR(100) NOT NULL,
  category  VARCHAR(32) NOT NULL,
  price     DECIMAL(10, 2) NOT NULL,
  stock     INT NOT NULL,
  weight_kg DECIMAL(6, 2) NOT NULL
);

CREATE TABLE orders (
  id          BIGINT AUTO_INCREMENT PRIMARY KEY,
  customer_id VARCHAR(32) NOT NULL,
  status      VARCHAR(16) NOT NULL,
  subtotal    DECIMAL(10, 2) NOT NULL,
  discount    DECIMAL(10, 2) NOT NULL,
  shipping    DECIMAL(10, 2) NOT NULL,
  total       DECIMAL(10, 2) NOT NULL,
  country     CHAR(2) NOT NULL,
  carrier     VARCHAR(32),
  payment_id  VARCHAR(40),
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) AUTO_INCREMENT = 1001;

CREATE TABLE order_lines (
  order_id BIGINT NOT NULL,
  sku      VARCHAR(16) NOT NULL,
  qty      INT NOT NULL,
  price    DECIMAL(10, 2) NOT NULL
);

INSERT INTO products VALUES
  ('KB-101', 'Mechanical keyboard', 'peripherals', 89.00, 25, 1.10),
  ('MS-220', 'Wireless mouse', 'peripherals', 39.00, 40, 0.20),
  ('WC-480', '4K webcam', 'peripherals', 119.00, 3, 0.25),
  ('MN-270', '27-inch 4K monitor', 'displays', 329.00, 8, 6.40),
  ('MN-340', '34-inch ultrawide monitor', 'displays', 549.00, 2, 8.90),
  ('HD-310', 'Noise-cancelling headphones', 'audio', 199.00, 12, 0.35),
  ('SP-045', 'Desk speakers', 'audio', 79.00, 15, 2.10),
  ('CB-005', 'USB-C cable', 'accessories', 9.00, 200, 0.05),
  ('LP-016', 'Laptop stand', 'accessories', 45.00, 0, 1.80),
  ('TB-220', 'Graphics tablet', 'peripherals', 159.00, 0, 0.90),
  ('DK-900', 'Thunderbolt dock', 'accessories', 249.00, 6, 0.60),
  ('MP-030', 'Desk mat', 'accessories', 25.00, 60, 0.40);
