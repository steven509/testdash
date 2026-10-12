package com.example.shop;

import static com.mongodb.client.model.Filters.eq;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.node.ObjectNode;
import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import java.time.Duration;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.bson.Document;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Disabled;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class CustomerEventsTest {
    private static Topic customerEvents;
    private static MongoClient mongo;

    @BeforeAll
    static void connect() {
        customerEvents = new Topic("customers.events", "customer-events-test");
        mongo = MongoClients.create("mongodb://localhost:27018");
    }

    @AfterAll
    static void disconnect() {
        customerEvents.close();
        mongo.close();
    }

    private static ObjectNode customer(String email) {
        return Shop.json.createObjectNode().put("email", email).put("name", "Ada Lovelace").put("marketingConsent", true);
    }

    @Test
    void registers_a_customer() {
        Shop.Response response = Shop.post("/customers", customer("ada@example.com"));

        assertEquals(201, response.status());
        assertTrue(response.body().path("id").asText().startsWith("c_"));
    }

    @Test
    void publishes_customer_registered() {
        String id = Shop.post("/customers", customer("grace@example.com")).body().path("id").asText();

        ConsumerRecord<String, String> event = customerEvents.await(id, Duration.ofSeconds(10));

        assertEquals("CustomerRegistered", Topic.header(event, "eventType"));
    }

    @Test
    void rejects_a_duplicate_email() {
        Shop.post("/customers", customer("alan@example.com"));

        assertEquals(409, Shop.post("/customers", customer("Alan@Example.com")).status());
    }

    @Test
    void stores_marketing_consent() {
        String id = Shop.post("/customers", customer("katherine@example.com")).body().path("id").asText();

        Document stored = mongo.getDatabase("shop").getCollection("customers").find(eq("_id", id)).first();

        assertEquals(true, stored.getBoolean("marketingConsent"));
    }

    @Test
    @Disabled("Waiting for the CRM sandbox")
    void exports_customers_to_the_crm() {
    }
}
