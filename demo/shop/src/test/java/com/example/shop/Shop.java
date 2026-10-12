package com.example.shop;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;

/** The shop as its clients see it: its API on localhost, and its database for checking what it stored. */
final class Shop {
    static final ObjectMapper json = new ObjectMapper();
    private static final HttpClient http = HttpClient.newHttpClient();
    private static final String URL = "http://localhost:18080";

    record Response(int status, JsonNode body) {
    }

    private Shop() {
    }

    static Response get(String path) {
        return send(HttpRequest.newBuilder(URI.create(URL + path)).GET().build());
    }

    static Response post(String path, JsonNode body) {
        return send(HttpRequest.newBuilder(URI.create(URL + path))
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(body.toString()))
                .build());
    }

    static ObjectNode order(String customerId, String... skuAndQty) {
        ObjectNode order = json.createObjectNode().put("customerId", customerId).put("country", "GB");
        for (int i = 0; i < skuAndQty.length; i += 2) {
            order.withArray("items").addObject().put("sku", skuAndQty[i]).put("qty", Integer.parseInt(skuAndQty[i + 1]));
        }
        return order;
    }

    /** Empties WireMock's request journal, as WireMock.resetAllRequests() does. */
    static void resetWireMockRequests() {
        try {
            http.send(HttpRequest.newBuilder(URI.create("http://localhost:18081/__admin/requests")).DELETE().build(),
                    HttpResponse.BodyHandlers.discarding());
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException(e);
        }
    }

    static Response placeOrder(ObjectNode order) {
        return post("/orders", order);
    }

    static int stockOf(String sku) {
        try (Connection db = DriverManager.getConnection("jdbc:mariadb://localhost:13306/shop", "shop", "shop");
             PreparedStatement query = db.prepareStatement("SELECT stock FROM products WHERE sku = ?")) {
            query.setString(1, sku);
            try (ResultSet rows = query.executeQuery()) {
                rows.next();
                return rows.getInt(1);
            }
        } catch (SQLException e) {
            throw new IllegalStateException(e);
        }
    }

    private static Response send(HttpRequest request) {
        try {
            HttpResponse<String> response = http.send(request, HttpResponse.BodyHandlers.ofString());
            return new Response(response.statusCode(), json.readTree(response.body()));
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException(e);
        }
    }
}
