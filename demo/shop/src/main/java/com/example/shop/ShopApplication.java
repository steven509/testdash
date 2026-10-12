package com.example.shop;

import com.example.shop.api.ShopApi;
import com.example.shop.audit.AuditLog;
import com.example.shop.catalog.ProductRepository;
import com.example.shop.customers.CustomerService;
import com.example.shop.db.Database;
import com.example.shop.events.EventPublisher;
import com.example.shop.logging.Log;
import com.example.shop.orders.OrderRepository;
import com.example.shop.orders.OrderService;
import com.example.shop.payments.PaymentClient;
import com.example.shop.shipping.ShippingClient;
import com.mongodb.client.MongoClients;
import com.mongodb.client.MongoDatabase;
import java.lang.management.ManagementFactory;

public final class ShopApplication {
    private static final Log log = Log.get(ShopApplication.class);
    private static final int PORT = 8080;

    public static void main(String[] args) throws Exception {
        long started = System.nanoTime();
        log.info("Starting ShopApplication v1.4.2 using Java %s with PID %d (/app/shop.jar started by %s in /app)",
                Runtime.version().feature() + "." + Runtime.version().interim() + "." + Runtime.version().update(),
                ProcessHandle.current().pid(), System.getProperty("user.name"));
        log.info("No active profile set, falling back to 1 default profile: \"default\"");

        Database db = Database.connect(env("DB_URL"), env("DB_USER"), env("DB_PASSWORD"), 4);
        log.info("Connected to MariaDB %s (pool of 4)", db.version());
        MongoDatabase mongo = MongoClients.create(env("MONGO_URI")).getDatabase("shop");
        log.info("Connected to MongoDB at %s", env("MONGO_URI"));
        EventPublisher events = new EventPublisher(env("KAFKA_BOOTSTRAP"));
        log.info("Kafka producer ready on %s", env("KAFKA_BOOTSTRAP"));

        AuditLog audit = new AuditLog(mongo);
        ProductRepository products = new ProductRepository(db);
        ShippingClient shipping = new ShippingClient(env("SHIPPING_URL"));
        OrderService orders = new OrderService(products, new OrderRepository(db), shipping,
                new PaymentClient(env("PAYMENTS_URL")), events, audit);
        new ShopApi(products, orders, new CustomerService(mongo, events, audit), shipping, events).listen(PORT);

        log.info("Tomcat started on port %d (http) with context path '/'", PORT);
        log.info("Started ShopApplication in %.3f seconds (process running for %.3f)",
                (System.nanoTime() - started) / 1e9, ManagementFactory.getRuntimeMXBean().getUptime() / 1e3);
    }

    private static String env(String name) {
        String value = System.getenv(name);
        if (value == null) {
            throw new IllegalStateException(name + " is not set");
        }
        return value;
    }
}
