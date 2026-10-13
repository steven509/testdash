package com.example.shop.db;

import com.example.shop.logging.Log;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Proxy;
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
            return work.apply(traced(connection));
        } catch (SQLException e) {
            throw new IllegalStateException(e);
        } finally {
            pool.add(connection);
        }
    }

    /** The connection, adding the request's trace to each statement as sqlcommenter does, for the database's log. */
    private static Connection traced(Connection connection) {
        String traceparent = Log.traceparent();
        if (traceparent == null) {
            return connection;
        }
        return (Connection) Proxy.newProxyInstance(Connection.class.getClassLoader(), new Class<?>[] {Connection.class},
                (proxy, method, args) -> {
                    if (method.getName().equals("prepareStatement") && args != null && args[0] instanceof String sql) {
                        args[0] = sql + " /*traceparent='" + traceparent + "'*/";
                    }
                    try {
                        return method.invoke(connection, args);
                    } catch (InvocationTargetException e) {
                        throw e.getCause();
                    }
                });
    }

    @FunctionalInterface
    public interface Work<T> {
        T apply(Connection connection) throws SQLException;
    }
}
