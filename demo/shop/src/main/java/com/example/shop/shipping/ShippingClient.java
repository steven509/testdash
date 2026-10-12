package com.example.shop.shipping;

import com.example.shop.api.ApiException;
import com.example.shop.logging.Log;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.math.BigDecimal;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

/** Asks the shipping aggregator for the cheapest carrier to a country. */
public final class ShippingClient {
    private static final Log log = Log.get(ShippingClient.class);
    private static final ObjectMapper json = new ObjectMapper();
    private static final int ATTEMPTS = 3;

    private final HttpClient http = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).build();
    private final String baseUrl;

    public ShippingClient(String baseUrl) {
        this.baseUrl = baseUrl;
    }

    public record Quote(String carrier, String service, BigDecimal price, int days) {
    }

    public Quote quote(String country, BigDecimal weightKg, String traceId) {
        for (int attempt = 1; ; attempt++) {
            HttpResponse<String> response = get("/shipping/quotes?country=" + country + "&weightKg=" + weightKg, traceId);
            JsonNode body = parse(response.body());
            if (response.statusCode() == 200) {
                return new Quote(body.path("carrier").asText(), body.path("service").asText(),
                        new BigDecimal(body.path("price").asText()), body.path("days").asInt());
            }
            if (response.statusCode() == 404) {
                throw new ApiException(422, "unsupported_country", "We do not ship to " + country);
            }
            if (attempt == ATTEMPTS) {
                log.warn("No shipping quote for %s after %d attempts", country, ATTEMPTS);
                throw new ApiException(503, "shipping_unavailable", "No carrier can quote for " + country + " right now");
            }
            log.warn("Shipping quote for %s failed with %d (%s), retrying (attempt %d of %d)", country,
                    response.statusCode(), body.path("error").asText("no reason"), attempt, ATTEMPTS);
        }
    }

    private HttpResponse<String> get(String path, String traceId) {
        HttpRequest request = HttpRequest.newBuilder(URI.create(baseUrl + path))
                .header("Accept", "application/json")
                .header("traceparent", "00-" + traceId + "-" + traceId.substring(0, 16) + "-01")
                .build();
        try {
            return http.send(request, HttpResponse.BodyHandlers.ofString());
        } catch (IOException e) {
            throw new ApiException(503, "shipping_unavailable", "The shipping aggregator is unreachable");
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException(e);
        }
    }

    private static JsonNode parse(String body) {
        try {
            return json.readTree(body.isBlank() ? "{}" : body);
        } catch (IOException e) {
            return json.createObjectNode();
        }
    }
}
