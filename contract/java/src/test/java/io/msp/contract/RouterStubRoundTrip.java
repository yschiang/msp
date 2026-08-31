package io.msp.contract;

import com.google.protobuf.ByteString;
import com.google.protobuf.Timestamp;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.msp.gen.serving.v1.ModelServiceGrpc;
import io.msp.gen.serving.v1.PredictRequest;
import io.msp.gen.serving.v1.PredictResponse;
import io.msp.gen.serving.v1.Status;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.TimeUnit;

/**
 * MSP-SPEC-001 §11 Phase 0 acceptance: "MYSVC 端以 Router stub 完成一次 predict
 * 往返". MYSVC is Java/SpringBoot on grpc-java (MYSVC-SPEC-001 D3), so this
 * round-trip is the only thing that proves the generated grpc-java bindings in
 * this module actually dial -- msp-traffic exercises the Go stubs, not these.
 *
 * <p>Deliberately a main class rather than a JUnit test: it needs router-stub
 * listening, which only the acceptance script can arrange, and the exit code is
 * the whole assertion. Test scope keeps it out of the published jar.
 *
 * <p>Usage: RouterStubRoundTrip &lt;host:port&gt; &lt;model-name&gt; &lt;golden-payload-file&gt;
 */
public final class RouterStubRoundTrip {

  public static void main(String[] args) throws Exception {
    if (args.length != 3) {
      System.err.println("usage: RouterStubRoundTrip <host:port> <model-name> <golden-payload-file>");
      System.exit(2);
    }
    String target = args[0];
    String modelName = args[1];
    byte[] payload = Files.readAllBytes(Path.of(args[2]));

    ManagedChannel channel = ManagedChannelBuilder.forTarget(target).usePlaintext().build();
    try {
      String requestId = UUID.randomUUID().toString();
      Instant now = Instant.now();
      PredictResponse resp =
          ModelServiceGrpc.newBlockingStub(channel)
              .withDeadlineAfter(30, TimeUnit.SECONDS)
              .predict(
                  PredictRequest.newBuilder()
                      .setRequestId(requestId)
                      .setDeviceId("mysvc-acceptance")
                      .setModelName(modelName)
                      .setIngestTime(
                          Timestamp.newBuilder()
                              .setSeconds(now.getEpochSecond())
                              .setNanos(now.getNano())
                              .build())
                      .setPayload(ByteString.copyFrom(payload))
                      .build());

      List<String> problems = new ArrayList<>();
      if (resp.getStatus() != Status.OK) {
        problems.add("status " + resp.getStatus() + ", want OK");
      }
      if (!requestId.equals(resp.getRequestId())) {
        problems.add("request_id \"" + resp.getRequestId() + "\" not echoed (sent \"" + requestId + "\")");
      }
      if (!modelName.equals(resp.getModelName())) {
        problems.add("model_name \"" + resp.getModelName() + "\", want \"" + modelName + "\"");
      }
      if (resp.getModelVersion().isEmpty()) {
        problems.add("model_version is empty");
      }
      if (resp.getPayload().isEmpty()) {
        problems.add("payload is empty");
      }

      if (!problems.isEmpty()) {
        System.err.println("  grpc-java round-trip FAILED: " + String.join("; ", problems));
        System.exit(1);
      }
      System.out.println(
          "  grpc-java round-trip OK: request_id echoed, model "
              + resp.getModelName()
              + " "
              + resp.getModelVersion()
              + ", "
              + resp.getPayload().size()
              + " payload bytes");
    } finally {
      channel.shutdownNow().awaitTermination(5, TimeUnit.SECONDS);
    }
  }

  private RouterStubRoundTrip() {}
}
