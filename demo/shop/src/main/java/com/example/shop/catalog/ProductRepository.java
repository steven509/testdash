package com.example.shop.catalog;

import com.example.shop.db.Database;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

public final class ProductRepository {
    private static final String COLUMNS = "SELECT sku, name, category, price, stock, weight_kg FROM products";

    private final Database db;

    public ProductRepository(Database db) {
        this.db = db;
    }

    public List<Product> list(String category) {
        return db.call(connection -> {
            PreparedStatement query = category == null
                    ? connection.prepareStatement(COLUMNS + " ORDER BY category, name")
                    : connection.prepareStatement(COLUMNS + " WHERE category = ? ORDER BY name");
            if (category != null) {
                query.setString(1, category);
            }
            List<Product> products = new ArrayList<>();
            try (ResultSet rows = query.executeQuery()) {
                while (rows.next()) {
                    products.add(product(rows));
                }
            }
            return products;
        });
    }

    public Optional<Product> find(String sku) {
        return db.call(connection -> {
            PreparedStatement query = connection.prepareStatement(COLUMNS + " WHERE sku = ?");
            query.setString(1, sku);
            try (ResultSet rows = query.executeQuery()) {
                return rows.next() ? Optional.of(product(rows)) : Optional.empty();
            }
        });
    }

    /** Takes qty off the stock, if there is that much. */
    public boolean reserve(String sku, int qty) {
        return db.call(connection -> {
            PreparedStatement update = connection.prepareStatement(
                    "UPDATE products SET stock = stock - ? WHERE sku = ? AND stock >= ?");
            update.setInt(1, qty);
            update.setString(2, sku);
            update.setInt(3, qty);
            return update.executeUpdate() == 1;
        });
    }

    public void release(String sku, int qty) {
        restock(sku, qty);
    }

    public void restock(String sku, int qty) {
        db.call(connection -> {
            PreparedStatement update = connection.prepareStatement("UPDATE products SET stock = stock + ? WHERE sku = ?");
            update.setInt(1, qty);
            update.setString(2, sku);
            return update.executeUpdate();
        });
    }

    private static Product product(ResultSet rows) throws SQLException {
        return new Product(rows.getString("sku"), rows.getString("name"), rows.getString("category"),
                rows.getBigDecimal("price"), rows.getInt("stock"), rows.getBigDecimal("weight_kg"));
    }
}
