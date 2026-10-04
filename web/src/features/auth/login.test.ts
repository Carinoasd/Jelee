import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/errors";
import { errorMessageKey } from "@/features/errors/messages";
import { createAppI18n } from "@/i18n";
import { useAuthStore } from "@/stores/auth";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, regularUser, type RouteFetch } from "@/test/routeFetch";
import { control } from "@/test/adminViews";

afterEach(() => {
  unmountAll();
});

const challenge = { secondFactorRequired: true, challenge: "c".repeat(43), expiresAt: "2026-10-04T00:05:00Z" };
const grant = {
  token: "opaque-token",
  csrf: "csrf-token",
  user: regularUser,
  session: { id: "40000000-0000-4000-8000-000000000001", userId: regularUser.id, clientKind: "web", deviceName: "Jelee Web", createdAt: "2026-10-04T00:00:00Z", expiresAt: "2026-10-05T00:00:00Z" },
};

function loginServer(): RouteFetch {
  return createRouteFetch()
    .on("POST", "/api/v1/auth/login", () => data(challenge))
    .on("POST", "/api/v1/auth/login/second-factor", () => data(grant))
    .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "system", density: "comfortable" }))
    .on("GET", "/api/v1/setup/status", () => data({ required: false }));
}

async function passwordStep(wrapper: VueWrapper) {
  await control(wrapper, "User name").setValue("kid");
  await control(wrapper, "Password").setValue("correct horse battery");
  await wrapper.find("form").trigger("submit");
  await flushPromises();
}

describe("login with a second factor", () => {
  it("asks for the authenticator code after the password and then signs in", async () => {
    const server = loginServer();
    const { wrapper, router, pinia } = await mountView("/login?redirect=/settings", { fetch: server.fetch });
    await passwordStep(wrapper);
    expect(useAuthStore(pinia).user).toBeNull();
    expect(wrapper.find("h1").text()).toBe("Two-step verification");
    const code = control(wrapper, "Verification code");
    expect(code.attributes("inputmode")).toBe("numeric");
    expect(code.attributes("autocomplete")).toBe("one-time-code");
    expect(document.activeElement).toBe(code.element);
    await code.setValue("123 456");
    await wrapper.find("form[data-second-factor]").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/auth/login/second-factor")[0]!.body).toEqual({ challenge: challenge.challenge, code: "123456" });
    expect(useAuthStore(pinia).user?.id).toBe(regularUser.id);
    await vi.waitFor(() => {
      expect(router.currentRoute.value.path).toBe("/settings");
    });
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("keeps the second step after a wrong code and accepts a recovery code", async () => {
    const server = loginServer().on("POST", "/api/v1/auth/login/second-factor", () => apiError(400, "invalid_two_factor_code"));
    const { wrapper, router } = await mountView("/login", { fetch: server.fetch });
    await passwordStep(wrapper);
    await control(wrapper, "Verification code").setValue("000000");
    await wrapper.find("form[data-second-factor]").trigger("submit");
    await flushPromises();
    expect(wrapper.find("[role=alert]").text()).toBe("The code is not correct or was already used. Wait for the next code and try again.");
    expect(wrapper.find("form[data-second-factor]").exists()).toBe(true);
    expect(router.currentRoute.value.name).toBe("login");

    server.on("POST", "/api/v1/auth/login/second-factor", () => data(grant));
    await wrapper.findAll("button").find((button) => button.text() === "Use a recovery code instead")!.trigger("click");
    await control(wrapper, "Recovery code").setValue(" abcd-efgh-ijkl-mnop ");
    await wrapper.find("form[data-second-factor]").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/auth/login/second-factor")[1]!.body).toEqual({
      challenge: challenge.challenge,
      recoveryCode: "abcd-efgh-ijkl-mnop",
    });
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).not.toBe("login");
    });
  });

  it("returns to the password step when the challenge is no longer valid", async () => {
    const server = loginServer().on("POST", "/api/v1/auth/login/second-factor", () => apiError(401, "login_challenge_invalid"));
    const { wrapper } = await mountView("/login", { fetch: server.fetch });
    await passwordStep(wrapper);
    await control(wrapper, "Verification code").setValue("123456");
    await wrapper.find("form[data-second-factor]").trigger("submit");
    await flushPromises();
    expect(wrapper.find("form[data-second-factor]").exists()).toBe(false);
    expect(wrapper.find("h1").text()).toBe("Sign in to Jelee");
    expect(wrapper.find("[role=alert]").text()).toBe("The verification step expired or failed too often. Sign in again with your password.");
    expect((control(wrapper, "Password").element as HTMLInputElement).value).toBe("");
  });

  it("checks the code before sending it and can go back to the password", async () => {
    const server = loginServer();
    const { wrapper } = await mountView("/login", { fetch: server.fetch });
    await passwordStep(wrapper);
    await control(wrapper, "Verification code").setValue("12a");
    await wrapper.find("form[data-second-factor]").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("Enter the 6-digit code.");
    expect(control(wrapper, "Verification code").attributes("aria-invalid")).toBe("true");
    expect(server.calls("POST", "/api/v1/auth/login/second-factor")).toHaveLength(0);
    await wrapper.findAll("button").find((button) => button.text() === "Back to the password")!.trigger("click");
    await flushPromises();
    expect(wrapper.find("h1").text()).toBe("Sign in to Jelee");
  });

  it("signs in at once when the account has no second factor", async () => {
    const server = loginServer().on("POST", "/api/v1/auth/login", () => data(grant));
    const { wrapper, router, pinia } = await mountView("/login", { fetch: server.fetch });
    await passwordStep(wrapper);
    expect(useAuthStore(pinia).user?.id).toBe(regularUser.id);
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).not.toBe("login");
    });
  });
});

describe("two-factor error codes", () => {
  it("map to localized catalog keys", () => {
    const key = (code: ConstructorParameters<typeof ApiError>[0]) => errorMessageKey(new ApiError(code, 400, "raw"));
    expect(key("invalid_two_factor_code")).toBe("twoFactor.errors.wrongCode");
    expect(key("login_challenge_invalid")).toBe("twoFactor.errors.challengeInvalid");
    expect(key("two_factor_unavailable")).toBe("twoFactor.errors.unavailable");
    expect(key("app_password_required")).toBe("twoFactor.errors.appPasswordRequired");
  });

  it("have messages that load with their screens, outside the entry catalogs", () => {
    expect(createAppI18n("en-US").global.te("twoFactor.errors.wrongCode")).toBe(false);
  });
});
