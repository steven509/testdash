package com.example.shop.db;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.SQLException;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;

/** A small pool of JDBC connections. */
public final class Database {
    private final BlockingQueue<Connection> pool;

    private Database(BlockingQueue<Connection> pool) {
        this.pool = pool;
    }

    public static Database connect(String url, String user, String password, int size) throws SQLException {
        BlockingQueue<Connection> pool = new ArrayBlockingQueue<>(size);
        for (int i = 0; i < size; i++) {
            pool.add(DriverManager.getConnection(url, user, password));
        }
        return new Database(pool);
    }

    public String version() {
        return call(connection -> connection.getMetaData().getDatabaseProductVersion());
    }

    public <T> T call(Work<T> work) {
        Connection connection;
        try {
            connection = pool.take();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException(e);
        }
        try {
            return work.apply(connection);
        } catch (SQLException e) {
            throw new IllegalStateException(e);
        } finally {
            pool.add(connection);
        }
    }

    @FunctionalInterface
    public interface Work<T> {
        T apply(Connection connection) throws SQLException;
    }
}
