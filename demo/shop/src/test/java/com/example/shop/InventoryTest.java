package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Duration;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class InventoryTest {

    @Nested
    class When_in_stock {
        @Test
        void reserves_stock_for_an_order() {
            int before = Shop.stockOf("KB-101");

            Shop.placeOrder(Shop.order("c_kim", "KB-101", "2"));

            assertEquals(before - 2, Shop.stockOf("KB-101"));
        }

        @Test
        void sells_the_last_item() {
            Shop.Response response = Shop.placeOrder(Shop.order("c_liam", "MN-340", "2"));

            assertEquals(201, response.status());
            assertEquals(0, Shop.stockOf("MN-340"));
        }
    }

    @Nested
    class When_out_of_stock {
        @Test
        void rejects_the_order_with_409() {
            assertEquals(409, Shop.placeOrder(Shop.order("c_mia", "LP-016", "1")).status());
        }

        @Test
        void names_the_missing_product() {
            Shop.Response response = Shop.placeOrder(Shop.order("c_mia", "LP-016", "1"));

            assertTrue(response.body().path("message").asText().contains("LP-016"));
        }

        @Test
        void keeps_the_stock_of_the_other_items() {
            int before = Shop.stockOf("MS-220");

            Shop.placeOrder(Shop.order("c_noah", "MS-220", "1", "LP-016", "1"));

            assertEquals(before, Shop.stockOf("MS-220"));
        }
    }

    @Nested
    class When_restocked {
        @Test
        void takes_orders_again() {
            Shop.post("/inventory/TB-220/restock", Shop.json.createObjectNode().put("qty", 5));

            assertEquals(201, Shop.placeOrder(Shop.order("c_olga", "TB-220", "1")).status());
        }

        @Test
        void publishes_stock_adjusted() {
            try (Topic inventory = new Topic("inventory.events", "inventory-test")) {
                Shop.post("/inventory/WC-480/restock", Shop.json.createObjectNode().put("qty", 10));

                ConsumerRecord<String, String> event = inventory.await("WC-480", Duration.ofSeconds(10));

                assertEquals("StockAdjusted", Topic.header(event, "eventType"));
            }
        }
    }
}
