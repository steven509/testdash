package com.example.shop.api;

import com.example.shop.billing.InvoiceRepository;
import com.example.shop.carts.CartService;
import com.example.shop.catalog.Product;
import com.example.shop.catalog.ProductCache;
import com.example.shop.catalog.ProductRepository;
import com.example.shop.customers.CustomerService;
import com.example.shop.events.EventPublisher;
import com.example.shop.logging.Log;
import com.example.shop.orders.OrderService;
import com.example.shop.shipping.ShippingClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.math.BigDecimal;
import java.net.InetSocketAddress;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;

/** The shop's HTTP API. */
public final class ShopApi {
    private static final Log log = Log.get(ShopApi.class);
    private static final ObjectMapper json = new ObjectMapper();
    private static final ThreadLocal<String> cacheStatus = new ThreadLocal<>(); // X-Cache: HIT or MISS

    private final ProductRepository products;
    private final ProductCache cache;
    private final OrderService orders;
    private final CartService carts;
    private final InvoiceRepository invoices;
    private final CustomerService customers;
    private final ShippingClient shipping;
    private final EventPublisher events;

    public ShopApi(ProductRepository products, ProductCache cache, OrderService orders, CartService carts,
            InvoiceRepository invoices, CustomerService customers, ShippingClient shipping, EventPublisher events) {
        this.products = products;
        this.cache = cache;
        this.orders = orders;
        this.carts = carts;
        this.invoices = invoices;
        this.customers = customers;
        this.shipping = shipping;
        this.events = events;
    }

    public void listen(int port) throws IOException {
        AtomicInteger threads = new AtomicInteger();
        HttpServer server = HttpServer.create(new InetSocketAddress(port), 64);
        server.setExecutor(Executors.newFixedThreadPool(8,
                task -> new Thread(task, "http-nio-" + port + "-exec-" + threads.incrementAndGet())));
        server.createContext("/", this::handle);
        server.start();
    }

    private void handle(HttpExchange exchange) throws IOException {
        String method = exchange.getRequestMethod();
        String path = exchange.getRequestURI().getPath();
        String traceId = Log.startTrace();
        cacheStatus.remove();
        int status;
        JsonNode body;
        try {
            JsonNode request = method.equals("POST") ? json.readTree(exchange.getRequestBody()) : null;
            body = route(method, path, query(exchange), request, traceId);
            status = method.equals("POST") && !path.endsWith("/cancel") ? 201 : 200;
        } catch (ApiException e) {
            status = e.status();
            body = json.createObjectNode().put("error", e.code()).put("message", e.getMessage());
        } catch (Exception e) {
            log.error("Request failed: " + method + " " + path, e);
            status = 500;
            body = json.createObjectNode().put("error", "internal").put("message", String.valueOf(e.getMessage()));
        } finally {
            Log.endTrace();
        }
        byte[] reply = body.toString().getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "application/json");
        exchange.getResponseHeaders().add("X-Trace-Id", traceId);
        if (cacheStatus.get() != null) {
            exchange.getResponseHeaders().add("X-Cache", cacheStatus.get());
        }
        exchange.sendResponseHeaders(status, reply.length);
        exchange.getResponseBody().write(reply);
        exchange.close();
    }

    private JsonNode route(String method, String path, Map<String, String> query, JsonNode request, String traceId) {
        String[] parts = path.substring(1).split("/");
        String route = method + " /" + parts[0] + (parts.length > 2 ? "/{}/" + parts[2] : parts.length == 2 ? "/{}" : "");
        return switch (route) {
            case "GET /actuator/{}" -> json.createObjectNode().put("status", "UP");
            case "GET /products" -> products(products.list(query.get("category")));
            case "GET /products/{}" -> cachedProduct(parts[1]);
            case "POST /orders" -> orders.place(request, traceId);
            case "GET /orders/{}" -> orders.get(Long.parseLong(parts[1]));
            case "POST /orders/{}/cancel" -> orders.cancel(Long.parseLong(parts[1]), traceId);
            case "POST /carts/{}/items" -> carts.add(parts[1], request);
            case "GET /carts/{}" -> carts.view(parts[1]);
            case "POST /carts/{}/checkout" -> carts.checkout(parts[1], request, traceId);
            case "GET /invoices/{}" -> invoices.find(Long.parseLong(parts[1]))
                    .orElseThrow(() -> new ApiException(404, "unknown_invoice", "There is no invoice for order " + parts[1]));
            case "POST /customers" -> customers.register(request, traceId);
            case "POST /inventory/{}/restock" -> restock(parts[1], request.path("qty").asInt(), traceId);
            case "GET /shipping/{}" -> quote(query, traceId);
            default -> throw new ApiException(404, "not_found", "No route for " + method + " " + path);
        };
    }

    private JsonNode restock(String sku, int qty, String traceId) {
        Product before = products.find(sku)
                .orElseThrow(() -> new ApiException(404, "unknown_product", "There is no product " + sku));
        products.restock(sku, qty);
        cache.evict(sku);
        events.publish("inventory.events", sku, "StockAdjusted", traceId, json.createObjectNode()
                .put("sku", sku)
                .put("change", qty)
                .put("stock", before.stock() + qty));
        log.info("Restocked %s: %d -> %d", sku, before.stock(), before.stock() + qty);
        return product(products.find(sku).orElseThrow());
    }

    private JsonNode cachedProduct(String sku) {
        ProductCache.Lookup lookup = cache.find(sku);
        cacheStatus.set(lookup.cached() ? "HIT" : "MISS");
        return product(lookup.product()
                .orElseThrow(() -> new ApiException(404, "unknown_product", "There is no product " + sku)));
    }

    private JsonNode quote(Map<String, String> query, String traceId) {
        String country = query.getOrDefault("country", "GB");
        ShippingClient.Quote quote = shipping.quote(country, new BigDecimal(query.getOrDefault("weightKg", "1.0")), traceId);
        return json.createObjectNode()
                .put("country", country)
                .put("carrier", quote.carrier())
                .put("service", quote.service())
                .put("price", quote.price().toPlainString())
                .put("days", quote.days());
    }

    private static ArrayNode products(Iterable<Product> products) {
        ArrayNode array = json.createArrayNode();
        products.forEach(product -> array.add(product(product)));
        return array;
    }

    private static ObjectNode product(Product product) {
        return json.createObjectNode()
                .put("sku", product.sku())
                .put("name", product.name())
                .put("category", product.category())
                .put("price", product.price().toPlainString())
                .put("stock", product.stock());
    }

    private static Map<String, String> query(HttpExchange exchange) {
        Map<String, String> query = new HashMap<>();
        String raw = exchange.getRequestURI().getRawQuery();
        if (raw != null) {
            for (String pair : raw.split("&")) {
                String[] keyValue = pair.split("=", 2);
                query.put(keyValue[0], keyValue.length > 1 ? URLDecoder.decode(keyValue[1], StandardCharsets.UTF_8) : "");
            }
        }
        return query;
    }
}
