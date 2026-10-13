package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import java.math.BigDecimal;
import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class TaxInvoiceTest {

    @Test
    void issues_an_invoice_for_each_order() {
        JsonNode order = Shop.placeOrder(Shop.order("c_gina", "KB-101", "1")).body();

        JsonNode invoice = Shop.get("/invoices/" + order.path("id").asLong()).body();

        assertTrue(invoice.path("number").asText().startsWith("INV-"), invoice.toString());
        assertEquals(order.path("total").asText(), invoice.path("total").asText());
    }

    @Test
    void includes_vat_at_20_percent() {
        long orderId = Shop.placeOrder(Shop.order("c_hugo", "CB-005", "4")).body().path("id").asLong();

        JsonNode invoice = Shop.get("/invoices/" + orderId).body();

        BigDecimal net = new BigDecimal(invoice.path("net").asText());
        BigDecimal vat = new BigDecimal(invoice.path("vat").asText());
        assertEquals(new BigDecimal(invoice.path("total").asText()), net.add(vat));
        assertEquals(net.multiply(new BigDecimal("0.2")).setScale(2, java.math.RoundingMode.HALF_UP), vat);
    }

    @Test
    void lists_what_was_ordered() {
        long orderId = Shop.placeOrder(Shop.order("c_ines", "MP-030", "2", "CB-005", "1")).body().path("id").asLong();

        JsonNode lines = Shop.get("/invoices/" + orderId).body().path("lines");

        assertEquals(2, lines.size());
        assertEquals("CB-005", lines.get(0).path("sku").asText());
        assertEquals(2, lines.get(1).path("qty").asInt());
    }

    @Test
    void has_no_invoice_for_an_unknown_order() {
        assertEquals(404, Shop.get("/invoices/9999999").status());
    }
}
