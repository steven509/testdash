package com.example.shop.carts;

import com.example.shop.api.ApiException;
import com.example.shop.catalog.Product;
import com.example.shop.catalog.ProductCache;
import com.example.shop.logging.Log;
import com.example.shop.orders.OrderService;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.math.BigDecimal;
import java.util.Map;
import java.util.TreeMap;
import redis.clients.jedis.Jedis;
import redis.clients.jedis.JedisPool;

/** Shopping carts, as Redis hashes of sku to quantity that expire a day after they last changed. */
public final class CartService {
    private static final Log log = Log.get(CartService.class);
    private static final ObjectMapper json = new ObjectMapper();
    private static final long TTL_SECONDS = 24 * 60 * 60;

    private final JedisPool redis;
    private final ProductCache products;
    private final OrderService orders;

    public CartService(JedisPool redis, ProductCache products, OrderService orders) {
        this.redis = redis;
        this.products = products;
        this.orders = orders;
    }

    public ObjectNode add(String cartId, JsonNode request) {
        String sku = request.path("sku").asText();
        int qty = request.path("qty").asInt(1);
        products.find(sku).product()
                .orElseThrow(() -> new ApiException(404, "unknown_product", "There is no product " + sku));
        try (Jedis jedis = redis.getResource()) {
            jedis.hincrBy(key(cartId), sku, qty);
            jedis.expire(key(cartId), TTL_SECONDS);
        }
        log.info("Added %d of %s to cart %s", qty, sku, cartId);
        return view(cartId);
    }

    public ObjectNode view(String cartId) {
        Map<String, String> items;
        long ttl;
        try (Jedis jedis = redis.getResource()) {
            items = new TreeMap<>(jedis.hgetAll(key(cartId)));
            ttl = jedis.ttl(key(cartId));
        }
        ObjectNode cart = json.createObjectNode().put("cartId", cartId);
        BigDecimal total = BigDecimal.ZERO.setScale(2);
        for (Map.Entry<String, String> item : items.entrySet()) {
            Product product = products.find(item.getKey()).product().orElseThrow();
            int qty = Integer.parseInt(item.getValue());
            cart.withArray("items").addObject()
                    .put("sku", product.sku())
                    .put("name", product.name())
                    .put("qty", qty)
                    .put("price", product.price().toPlainString());
            total = total.add(product.price().multiply(BigDecimal.valueOf(qty)));
        }
        return cart.put("total", total.toPlainString()).put("expiresInSeconds", Math.max(0, ttl));
    }

    /** Places an order for what is in the cart, and empties it. */
    public ObjectNode checkout(String cartId, JsonNode request, String traceId) {
        Map<String, String> items;
        try (Jedis jedis = redis.getResource()) {
            items = new TreeMap<>(jedis.hgetAll(key(cartId)));
        }
        if (items.isEmpty()) {
            throw new ApiException(400, "empty_cart", "Cart " + cartId + " is empty");
        }
        ObjectNode order = json.createObjectNode()
                .put("customerId", request.path("customerId").asText("guest"))
                .put("country", request.path("country").asText("GB"));
        items.forEach((sku, qty) -> order.withArray("items").addObject().put("sku", sku).put("qty", Integer.parseInt(qty)));
        ObjectNode placed = orders.place(order, traceId);
        try (Jedis jedis = redis.getResource()) {
            jedis.del(key(cartId));
        }
        log.info("Cart %s checked out as order %d", cartId, placed.path("id").asLong());
        return placed;
    }

    private static String key(String cartId) {
        return "cart:" + cartId;
    }
}
