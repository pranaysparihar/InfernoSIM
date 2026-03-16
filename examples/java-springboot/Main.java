// InfernoSIM example: Java Spring Boot service
// Run: mvn spring-boot:run  (or: java -jar target/app.jar)
package com.example.infernosim;

import org.springframework.boot.*;
import org.springframework.boot.autoconfigure.*;
import org.springframework.web.bind.annotation.*;
import java.util.*;

@SpringBootApplication
@RestController
public class Main {
    private final Map<String, String> tokens = new HashMap<>();

    public static void main(String[] args) { SpringApplication.run(Main.class, args); }

    @PostMapping("/login")
    Map<String, String> login(@RequestBody Map<String, String> body) {
        String token = "tok_" + body.getOrDefault("user", "anon") + "_secret";
        tokens.put(token, body.getOrDefault("user", "anon"));
        return Map.of("access_token", token);
    }

    @GetMapping("/orders")
    Object orders(@RequestHeader(value = "Authorization", defaultValue = "") String auth) {
        String token = auth.replace("Bearer ", "");
        if (!tokens.containsKey(token)) return Map.of("error", "unauthorized");
        return Map.of("orders", List.of(Map.of("id", "order_001", "status", "confirmed")));
    }
}
