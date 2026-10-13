package com.example.shop.catalog;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.Optional;
import redis.clients.jedis.Jedis;
import redis.clients.jedis.JedisPool;

/** Products by sku, kept in Redis for five minutes in front of the database. */
public final class ProductCache {
    private static final ObjectMapper json = new ObjectMapper();
    private static final long TTL_SECONDS = 300;

    private final JedisPool redis;
    private final ProductRepository products;

    public ProductCache(JedisPool redis, ProductRepository products) {
        this.redis = redis;
        this.products = products;
    }

    public record Lookup(Optional<Product> product, boolean cached) {
    }

    public Lookup find(String sku) {
        try (Jedis jedis = redis.getResource()) {
            String hit = jedis.get(key(sku));
            if (hit != null) {
                return new Lookup(Optional.of(json.readValue(hit, Product.class)), true);
            }
            Optional<Product> product = products.find(sku);
            if (product.isPresent()) {
                jedis.setex(key(sku), TTL_SECONDS, json.writeValueAsString(product.get()));
            }
            return new Lookup(product, false);
        } catch (JsonProcessingException e) {
            throw new IllegalStateException(e);
        }
    }

    /** Forgets a product whose stock changed. */
    public void evict(String sku) {
        try (Jedis jedis = redis.getResource()) {
            jedis.del(key(sku));
        }
    }

    private static String key(String sku) {
        return "product:" + sku;
    }
}
