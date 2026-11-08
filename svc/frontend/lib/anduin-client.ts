import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AnduinService } from "@/gen/hacktheburgh/v1/service_pb";

let client: Client<typeof AnduinService> | undefined;

export function getAnduinClient() {
  if (typeof window === "undefined") {
    throw new Error("The Anduin Connect client can only be used in the browser.");
  }

  client ??= createClient(
    AnduinService,
    createConnectTransport({ baseUrl: `${window.location.origin}/rpc` }),
  );
  return client;
}
