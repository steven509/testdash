package com.example.shop.customers;

import static com.mongodb.client.model.Filters.eq;

import com.example.shop.api.ApiException;
import com.example.shop.audit.AuditLog;
import com.example.shop.events.EventPublisher;
import com.example.shop.logging.Log;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.mongodb.client.MongoCollection;
import com.mongodb.client.MongoDatabase;
import java.util.Date;
import java.util.HexFormat;
import java.util.concurrent.ThreadLocalRandom;
import org.bson.Document;

public final class CustomerService {
    private static final Log log = Log.get(CustomerService.class);
    private static final ObjectMapper json = new ObjectMapper();

    private final MongoCollection<Document> customers;
    private final EventPublisher events;
    private final AuditLog audit;

    public CustomerService(MongoDatabase database, EventPublisher events, AuditLog audit) {
        this.customers = database.getCollection("customers");
        this.events = events;
        this.audit = audit;
    }

    public ObjectNode register(JsonNode request, String traceId) {
        String email = request.path("email").asText().trim().toLowerCase();
        if (!email.contains("@")) {
            throw new ApiException(400, "invalid_email", "An email address is needed");
        }
        if (customers.find(eq("email", email)).first() != null) {
            log.warn("A customer with %s already exists", email);
            throw new ApiException(409, "duplicate_email", "That email address is already registered");
        }
        String id = "c_" + HexFormat.of().toHexDigits(ThreadLocalRandom.current().nextInt()).substring(0, 6);
        Log.context("customer", id);
        boolean consent = request.path("marketingConsent").asBoolean(false);
        customers.insertOne(new Document("_id", id)
                .append("email", email)
                .append("name", request.path("name").asText())
                .append("marketingConsent", consent)
                .append("createdAt", new Date()));
        events.publish("customers.events", id, "CustomerRegistered", traceId, json.createObjectNode()
                .put("customerId", id)
                .put("email", email)
                .put("marketingConsent", consent));
        audit.record("customer.registered", id, id, traceId);
        log.info("Registered customer %s", id);
        return json.createObjectNode().put("id", id).put("email", email);
    }
}
