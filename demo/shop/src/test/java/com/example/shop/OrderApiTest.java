package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.fasterxml.jackson.databind.node.ObjectNode;
import java.time.Duration;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class OrderApiTest {
    private static Topic orderEvents;

    @BeforeAll
    static void followOrderEvents() {
        orderEvents = new Topic("orders.events", "order-api-test");
    }

    @AfterAll
    static void stopFollowing() {
        orderEvents.close();
    }

    @Test
    void places_an_order() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_alice", "KB-101", "1"));

        assertEquals(201, response.status(), response.body().toString());
        assertEquals("CONFIRMED", response.body().path("status").asText());
        assertEquals("93.99", response.body().path("total").asText());
    }

    @Test
    void returns_an_order_by_its_id() {
        long id = Shop.placeOrder(Shop.order("c_bob", "MS-220", "2")).body().path("id").asLong();

        Shop.Response response = Shop.get("/orders/" + id);

        assertEquals(200, response.status());
        assertEquals("c_bob", response.body().path("customerId").asText());
        assertEquals(2, response.body().path("lines").get(0).path("qty").asInt());
    }

    @Test
    void rejects_an_empty_basket() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_carol"));

        assertEquals(400, response.status());
        assertEquals("empty_basket", response.body().path("error").asText());
    }

    @Test
    void rejects_unknown_products() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_carol", "XX-000", "1"));

        assertEquals(404, response.status());
    }

    @Test
    void applies_a_loyalty_discount() {
        ObjectNode order = Shop.order("c_dave", "SP-045", "1", "MP-030", "1").put("couponCode", "loyalty10");

        Shop.Response response = Shop.placeOrder(order);

        assertEquals(201, response.status());
        assertEquals("10.40", response.body().path("discount").asText(), "loyalty discount");
    }

    @Test
    void ships_free_over_one_hundred_pounds() {
        Shop.Response response = Shop.placeOrder(Shop.order("c_erin", "MN-270", "1"));

        assertEquals("0.00", response.body().path("shipping").asText());
        assertEquals("329.00", response.body().path("total").asText());
    }

    @Test
    void publishes_order_placed() {
        String id = Shop.placeOrder(Shop.order("c_frank", "CB-005", "3")).body().path("id").asText();

        ConsumerRecord<String, String> event = orderEvents.await(id, Duration.ofSeconds(10));

        assertEquals("OrderPlaced", Topic.header(event, "eventType"));
        assertEquals("shop", Topic.header(event, "source"));
    }
}
