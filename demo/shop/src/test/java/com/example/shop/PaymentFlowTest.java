package com.example.shop;

import static com.mongodb.client.model.Filters.and;
import static com.mongodb.client.model.Filters.eq;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import org.bson.Document;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class PaymentFlowTest {
    private static MongoClient mongo;

    @BeforeAll
    static void connect() {
        mongo = MongoClients.create("mongodb://localhost:27018");
    }

    @AfterAll
    static void disconnect() {
        mongo.close();
    }

    @BeforeEach
    void forgetEarlierRequests() {
        Shop.resetWireMockRequests();
    }

    @Test
    void authorises_card_payments() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_grace", "HD-310", "1"));

        assertEquals(201, response.status());
        assertTrue(response.body().path("paymentId").asText().startsWith("pay_"));
    }

    @Test
    void returns_402_when_the_card_is_declined() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_heidi", "SP-045", "1")
                .put("paymentSource", "tok_insufficient_funds"));

        assertEquals(402, response.status());
        assertEquals("payment_declined", response.body().path("error").asText());
    }

    @Test
    void releases_stock_when_the_payment_fails() {
        int before = Shop.stockOf("WC-480");

        Shop.placeOrder(Shop.order("c_heidi", "WC-480", "1").put("paymentSource", "tok_insufficient_funds"));

        assertEquals(before, Shop.stockOf("WC-480"));
    }

    @Test
    void refunds_the_payment_when_cancelled() {
        long id = Shop.placeOrder(Shop.order("c_ivan", "DK-900", "1")).body().path("id").asLong();

        Shop.Response response = Shop.post("/orders/" + id + "/cancel", Shop.json.createObjectNode());

        assertEquals(200, response.status(), "cancel order " + id + ": " + response.body());
        assertEquals("CANCELLED", response.body().path("status").asText());
    }

    @Test
    void records_the_payment_in_the_audit_log() {
        String id = Shop.placeOrder(Shop.order("c_judy", "MP-030", "2")).body().path("id").asText();

        Document entry = mongo.getDatabase("shop").getCollection("audit")
                .find(and(eq("action", "order.placed"), eq("entity", id))).first();

        assertNotNull(entry, "audit entry for order " + id);
        assertEquals("c_judy", entry.getString("actor"));
    }
}
