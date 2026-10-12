package com.example.shop.payments;

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
import java.time.Duration;
import java.util.UUID;

/** The card payment provider's API. */
public final class PaymentClient {
    private static final Log log = Log.get(PaymentClient.class);
    private static final ObjectMapper json = new ObjectMapper();

    private final HttpClient http = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(2)).build();
    private final String baseUrl;

    public PaymentClient(String baseUrl) {
        this.baseUrl = baseUrl;
    }

    public record Payment(String id, String status) {
    }

    public Payment authorise(BigDecimal amount, String source, String reference, String traceId) {
        JsonNode reply = post("/payments", json.createObjectNode()
                .put("amount", amount.toPlainString())
                .put("currency", "GBP")
                .put("source", source)
                .put("reference", reference), traceId);
        if (reply.path("status").asText().equals("DECLINED")) {
            String reason = reply.path("declineCode").asText();
            log.warn("Card payment of £%s declined: %s", amount.toPlainString(), reason);
            throw new ApiException(402, "payment_declined", "The card was declined: " + reason);
        }
        Payment payment = new Payment(reply.path("id").asText(), reply.path("status").asText());
        log.info("Payment %s authorised for £%s", payment.id(), amount.toPlainString());
        return payment;
    }

    public void refund(String paymentId, BigDecimal amount, String traceId) {
        HttpResponse<String> response = send("/payments/" + paymentId + "/refund", json.createObjectNode()
                .put("amount", amount.toPlainString()), "text/plain", traceId);
        if (response.statusCode() != 200) {
            throw new PaymentGatewayException("Refund of " + paymentId + " failed: HTTP " + response.statusCode()
                    + " from the payment provider");
        }
        log.info("Refunded £%s to %s", amount.toPlainString(), paymentId);
    }

    private JsonNode post(String path, JsonNode body, String traceId) {
        HttpResponse<String> response = send(path, body, "application/json", traceId);
        try {
            return json.readTree(response.body());
        } catch (IOException e) {
            throw new PaymentGatewayException("Unreadable reply from the payment provider: " + e.getMessage());
        }
    }

    private HttpResponse<String> send(String path, JsonNode body, String contentType, String traceId) {
        HttpRequest request = HttpRequest.newBuilder(URI.create(baseUrl + path))
                .header("Content-Type", contentType)
                .header("Idempotency-Key", UUID.randomUUID().toString())
                .header("traceparent", "00-" + traceId + "-" + traceId.substring(0, 16) + "-01")
                .POST(HttpRequest.BodyPublishers.ofString(body.toString()))
                .build();
        try {
            return http.send(request, HttpResponse.BodyHandlers.ofString());
        } catch (IOException e) {
            throw new PaymentGatewayException("The payment provider is unreachable: " + e.getMessage());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new PaymentGatewayException("Interrupted");
        }
    }
}
