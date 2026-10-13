package com.example.shop.billing;

import com.example.shop.db.Database;
import com.example.shop.orders.Order;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.math.BigDecimal;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.List;
import java.util.Optional;

/** The invoices, in the billing database (Postgres). A total includes UK VAT at 20%. */
public final class InvoiceRepository {
    private static final ObjectMapper json = new ObjectMapper();

    private final Database db;

    public InvoiceRepository(Database db) {
        this.db = db;
    }

    /** Issues an order's invoice and returns its number. */
    public String issue(long orderId, String customerId, BigDecimal total, List<Order.Line> lines) {
        return db.call(connection -> {
            connection.setAutoCommit(false);
            try {
                PreparedStatement insert = connection.prepareStatement(
                        "INSERT INTO invoices (order_id, customer_id, net, vat, total) "
                                + "VALUES (?, ?, round(? / 1.2, 2), ? - round(? / 1.2, 2), ?) RETURNING id, number");
                insert.setLong(1, orderId);
                insert.setString(2, customerId);
                insert.setBigDecimal(3, total);
                insert.setBigDecimal(4, total);
                insert.setBigDecimal(5, total);
                insert.setBigDecimal(6, total);
                long id;
                String number;
                try (ResultSet keys = insert.executeQuery()) {
                    keys.next();
                    id = keys.getLong("id");
                    number = keys.getString("number");
                }
                PreparedStatement line = connection.prepareStatement(
                        "INSERT INTO invoice_lines (invoice_id, sku, qty, price) VALUES (?, ?, ?, ?)");
                for (Order.Line item : lines) {
                    line.setLong(1, id);
                    line.setString(2, item.sku());
                    line.setInt(3, item.qty());
                    line.setBigDecimal(4, item.price());
                    line.addBatch();
                }
                line.executeBatch();
                connection.commit();
                return number;
            } catch (SQLException e) {
                connection.rollback();
                throw e;
            } finally {
                connection.setAutoCommit(true);
            }
        });
    }

    public Optional<ObjectNode> find(long orderId) {
        return db.call(connection -> {
            PreparedStatement query = connection.prepareStatement(
                    "SELECT i.number, i.net, i.vat, i.total, l.sku, l.qty, l.price "
                            + "FROM invoices i JOIN invoice_lines l ON l.invoice_id = i.id "
                            + "WHERE i.order_id = ? ORDER BY l.sku");
            query.setLong(1, orderId);
            ObjectNode invoice = null;
            try (ResultSet rows = query.executeQuery()) {
                while (rows.next()) {
                    if (invoice == null) {
                        invoice = json.createObjectNode()
                                .put("number", rows.getString("number"))
                                .put("orderId", orderId)
                                .put("net", rows.getBigDecimal("net").toPlainString())
                                .put("vat", rows.getBigDecimal("vat").toPlainString())
                                .put("total", rows.getBigDecimal("total").toPlainString());
                    }
                    invoice.withArray("lines").addObject()
                            .put("sku", rows.getString("sku"))
                            .put("qty", rows.getInt("qty"))
                            .put("price", rows.getBigDecimal("price").toPlainString());
                }
            }
            return Optional.ofNullable(invoice);
        });
    }
}
