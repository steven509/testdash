package com.example.shop.orders;

import com.example.shop.db.Database;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

public final class OrderRepository {
    private final Database db;

    public OrderRepository(Database db) {
        this.db = db;
    }

    public long insert(Order order) {
        return db.call(connection -> {
            PreparedStatement insert = connection.prepareStatement("INSERT INTO orders (customer_id, status, subtotal, "
                    + "discount, shipping, total, country, carrier, payment_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    Statement.RETURN_GENERATED_KEYS);
            insert.setString(1, order.customerId());
            insert.setString(2, order.status());
            insert.setBigDecimal(3, order.subtotal());
            insert.setBigDecimal(4, order.discount());
            insert.setBigDecimal(5, order.shipping());
            insert.setBigDecimal(6, order.total());
            insert.setString(7, order.country());
            insert.setString(8, order.carrier());
            insert.setString(9, order.paymentId());
            insert.executeUpdate();
            long id;
            try (ResultSet keys = insert.getGeneratedKeys()) {
                keys.next();
                id = keys.getLong(1);
            }
            PreparedStatement line = connection.prepareStatement(
                    "INSERT INTO order_lines (order_id, sku, qty, price) VALUES (?, ?, ?, ?)");
            for (Order.Line l : order.lines()) {
                line.setLong(1, id);
                line.setString(2, l.sku());
                line.setInt(3, l.qty());
                line.setBigDecimal(4, l.price());
                line.addBatch();
            }
            line.executeBatch();
            return id;
        });
    }

    public Optional<Order> find(long id) {
        return db.call(connection -> {
            PreparedStatement lineQuery = connection.prepareStatement(
                    "SELECT sku, qty, price FROM order_lines WHERE order_id = ?");
            lineQuery.setLong(1, id);
            List<Order.Line> lines = new ArrayList<>();
            try (ResultSet rows = lineQuery.executeQuery()) {
                while (rows.next()) {
                    lines.add(new Order.Line(rows.getString("sku"), rows.getInt("qty"), rows.getBigDecimal("price")));
                }
            }
            PreparedStatement query = connection.prepareStatement("SELECT * FROM orders WHERE id = ?");
            query.setLong(1, id);
            try (ResultSet rows = query.executeQuery()) {
                if (!rows.next()) {
                    return Optional.empty();
                }
                return Optional.of(new Order(id, rows.getString("customer_id"), rows.getString("status"),
                        rows.getBigDecimal("subtotal"), rows.getBigDecimal("discount"), rows.getBigDecimal("shipping"),
                        rows.getBigDecimal("total"), rows.getString("country"), rows.getString("carrier"),
                        rows.getString("payment_id"), lines));
            }
        });
    }

    public void updateStatus(long id, String status) {
        db.call(connection -> {
            PreparedStatement update = connection.prepareStatement("UPDATE orders SET status = ? WHERE id = ?");
            update.setString(1, status);
            update.setLong(2, id);
            return update.executeUpdate();
        });
    }
}
