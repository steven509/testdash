package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.UUID;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class ShoppingCartTest {

    private final String cart = "/carts/" + UUID.randomUUID().toString().substring(0, 8);

    @Test
    void adds_items_to_a_cart() {
        Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "MP-030").put("qty", 1));
        JsonNode body = Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "MP-030").put("qty", 2)).body();

        assertEquals(3, body.path("items").get(0).path("qty").asInt());
        assertEquals("75.00", body.path("total").asText());
    }

    @Test
    void keeps_a_cart_for_a_day() {
        JsonNode body = Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "CB-005").put("qty", 1)).body();

        assertTrue(body.path("expiresInSeconds").asLong() > 86_000, body.toString());
    }

    @Test
    void rejects_an_unknown_product() {
        assertEquals(404, Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "XX-000")).status());
    }

    @Test
    void checks_out_a_cart_into_an_order() {
        Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "KB-101").put("qty", 1));
        Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", "CB-005").put("qty", 2));

        Shop.Response order = Shop.post(cart + "/checkout", Shop.json.createObjectNode().put("customerId", "c_jo"));

        assertEquals(201, order.status(), order.body().toString());
        assertEquals(2, order.body().path("lines").size());
        assertTrue(Shop.get(cart).body().path("items").isMissingNode(), "the cart is emptied");
    }

    @Test
    void serves_a_product_from_the_cache() {
        Shop.post("/inventory/DK-900/restock", Shop.json.createObjectNode().put("qty", 1)); // which evicts it

        assertEquals("MISS", Shop.cacheStatus("/products/DK-900"));
        assertEquals("HIT", Shop.cacheStatus("/products/DK-900"));
    }
}
