import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { captureDownloads } from "@/test/downloads";
import { button, control, toastKeys } from "@/test/adminViews";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, json, noContent, regularUser, type RouteFetch } from "@/test/routeFetch";
import type { AppPassword, TwoFactorStatus } from "./twoFactorApi";

afterEach(() => {
  unmountAll();
  Reflect.deleteProperty(navigator, "clipboard");
});

const off: TwoFactorStatus = { available: true, enabled: false, pending: false, recoveryCodesRemaining: 0 };
const on: TwoFactorStatus = { available: true, enabled: true, enabledAt: "2026-10-04T00:00:00Z", pending: false, recoveryCodesRemaining: 10 };
const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
const uri = `otpauth://totp/Jelee:kid?secret=${secret}&issuer=Jelee&algorithm=SHA1&digits=6&period=30`;
const recoveryCodes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-ijkl-${String(i).padStart(4, "0")}`);
const phone: AppPassword = { id: "50000000-0000-4000-8000-000000000001", name: "Phone", createdAt: "2026-10-01T00:00:00Z", lastUsedAt: "2026-10-03T00:00:00Z" };

function settingsServer(status: TwoFactorStatus = off, passwords: AppPassword[] = []): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/users/me/preferences", () => data({ theme: "system", density: "comfortable" }))
    .on("GET", "/api/v1/setup/status", () => data({ required: false }))
    .on("GET", `/api/v1/users/${regularUser.id}/two-factor`, () => data(status))
    .on("GET", `/api/v1/users/${regularUser.id}/app-passwords`, () => data(passwords));
}

async function open(server: RouteFetch) {
  return mountView("/settings", { fetch: server.fetch, user: regularUser, csrf: "csrf-token" });
}

/** The input labelled text inside one form (several forms share labels). */
function inputIn(form: ReturnType<VueWrapper["find"]>, text: string) {
  const id = form.findAll("label").find((label) => label.text() === text)?.attributes("for");
  if (id === undefined) {
    throw new Error("no field " + text);
  }
  return form.find(`[id="${id}"]`);
}

function section(wrapper: VueWrapper, titleId: string) {
  const found = wrapper.find(`section[aria-labelledby="${titleId}"]`);
  expect(found.exists()).toBe(true);
  return found;
}

describe("two-step verification settings", () => {
  it("sets up the authenticator with a QR code and shows the recovery codes once", async () => {
    let status = off;
    const server = settingsServer()
      .on("GET", `/api/v1/users/${regularUser.id}/two-factor`, () => data(status))
      .on("POST", "/api/v1/users/me/two-factor/enroll", () => data({ secret, uri }))
      .on("POST", "/api/v1/users/me/two-factor/confirm", () => {
        status = on;
        return data({ recoveryCodes });
      });
    const { wrapper } = await open(server);
    const card = section(wrapper, "settings-two-factor");
    expect(card.text()).toContain("Off");
    await button(wrapper, "Set up two-step verification").trigger("click");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/two-factor/enroll")[0]!.body).toEqual({});
    expect(document.activeElement?.id).toBe("two-factor-setup-title");

    // Inline SVG QR code with a quiet zone, plus the key and link as text.
    const svg = card.find("svg[role=img]");
    expect(svg.attributes("aria-label")).toBe("QR code for the authenticator app");
    expect(svg.attributes("style")).toBeUndefined();
    const size = Number(svg.attributes("data-version")) * 4 + 17 + 8;
    expect(svg.attributes("viewBox")).toBe(`0 0 ${size} ${size}`);
    expect(svg.find("path").attributes("d")).toMatch(/^M4 4h7v1h-7z/);
    expect(card.find("[data-secret]").text()).toBe("JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP");
    expect(card.find("[data-uri]").text()).toBe(uri);
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await button(wrapper, "Copy key").trigger("click");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith(secret);
    expect(card.text()).toContain("Copied to the clipboard.");

    await control(wrapper, "Verification code").setValue("123 456");
    await card.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/two-factor/confirm")[0]!.body).toEqual({ code: "123456" });
    expect(card.text()).toContain("Every other browser and device of your account has been signed out.");
    expect(card.text()).toContain("application password");
    expect(card.findAll("[data-recovery-codes] li").map((item) => item.text())).toEqual(recoveryCodes);
    expect(document.activeElement?.id).toBe("two-factor-codes-title");

    const capture = captureDownloads();
    try {
      await button(wrapper, "Download as .txt").trigger("click");
      expect(capture.downloads[0]!.fileName).toBe("jelee-recovery-codes.txt");
      const text = await capture.downloads[0]!.blob.text();
      expect(text).toContain("Jelee recovery codes for kid");
      for (const code of recoveryCodes) {
        expect(text).toContain(code);
      }
    } finally {
      capture.restore();
    }

    // Closing needs the explicit confirmation; afterwards the codes are gone.
    expect(button(wrapper, "Done").attributes("disabled")).toBeDefined();
    await control(wrapper, "I have saved these codes").setValue(true);
    await button(wrapper, "Done").trigger("click");
    await flushPromises();
    expect(card.find("[data-recovery-codes]").exists()).toBe(false);
    expect(card.text()).toContain("On");
    expect(card.text()).toContain("Unused recovery codes: 10");
    expect(wrapper.html()).not.toContain(recoveryCodes[0]);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("keeps the setup open after a wrong code", async () => {
    const server = settingsServer()
      .on("POST", "/api/v1/users/me/two-factor/enroll", () => data({ secret, uri }))
      .on("POST", "/api/v1/users/me/two-factor/confirm", () => apiError(400, "invalid_two_factor_code"));
    const { wrapper } = await open(server);
    await button(wrapper, "Set up two-step verification").trigger("click");
    await flushPromises();
    await control(wrapper, "Verification code").setValue("12345");
    await section(wrapper, "settings-two-factor").find("form").trigger("submit");
    expect(wrapper.text()).toContain("Enter the 6-digit code.");
    expect(server.calls("POST", "/api/v1/users/me/two-factor/confirm")).toHaveLength(0);
    await control(wrapper, "Verification code").setValue("654321");
    await section(wrapper, "settings-two-factor").find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("The code is not correct or was already used.");
    expect(section(wrapper, "settings-two-factor").find("svg").exists()).toBe(true);
  });

  it("explains that the server cannot offer it without a master key", async () => {
    const server = settingsServer({ ...off, available: false });
    const { wrapper } = await open(server);
    const card = section(wrapper, "settings-two-factor");
    expect(card.text()).toContain("the server has no master key configured");
    expect(card.findAll("button").some((candidate) => candidate.text() === "Set up two-step verification")).toBe(false);
  });

  it("generates new recovery codes with a current code", async () => {
    const server = settingsServer({ ...on, recoveryCodesRemaining: 2 }).on("POST", "/api/v1/users/me/two-factor/recovery-codes", () =>
      data({ recoveryCodes }),
    );
    const { wrapper } = await open(server);
    const card = section(wrapper, "settings-two-factor");
    expect(card.text()).toContain("Few recovery codes are left.");
    const form = card.find('form[aria-labelledby="two-factor-regenerate-title"]');
    await form.find("input").setValue("111111");
    await form.trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/two-factor/recovery-codes")[0]!.body).toEqual({ code: "111111" });
    expect(card.findAll("[data-recovery-codes] li")).toHaveLength(10);
    expect(card.text()).not.toContain("has been signed out");
  });

  it("turns it off with the password and a recovery code", async () => {
    let status = on;
    const server = settingsServer()
      .on("GET", `/api/v1/users/${regularUser.id}/two-factor`, () => data(status))
      .on("POST", "/api/v1/users/me/two-factor/disable", ({ body }) => {
        if ((body as { password: string }).password !== "right password") {
          return apiError(400, "invalid_password");
        }
        status = off;
        return noContent();
      });
    const { wrapper } = await open(server);
    const form = section(wrapper, "settings-two-factor").find('form[aria-labelledby="two-factor-disable-title"]');
    await form.trigger("submit");
    expect(form.text()).toContain("Enter your password.");
    expect(form.text()).toContain("Enter the 6-digit code.");
    expect(server.calls("POST", "/api/v1/users/me/two-factor/disable")).toHaveLength(0);

    await inputIn(form, "Account password").setValue("wrong password");
    await inputIn(form, "Verification code").setValue("123456");
    await form.trigger("submit");
    await flushPromises();
    expect(form.find("[role=alert]").text()).toBe("The current password is not correct.");

    await inputIn(form, "Account password").setValue("right password");
    await button(wrapper, "Use a recovery code instead").trigger("click");
    await inputIn(form, "Recovery code").setValue("abcd-efgh-ijkl-0001");
    await form.trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/two-factor/disable")[1]!.body).toEqual({
      password: "right password",
      recoveryCode: "abcd-efgh-ijkl-0001",
    });
    expect(toastKeys(wrapper)).toContain("twoFactor.settings.disabled");
    expect(section(wrapper, "settings-two-factor").text()).toContain("Set up two-step verification");
  });
});

describe("application passwords", () => {
  it("creates one, shows it once and revokes it", async () => {
    const created = { id: "50000000-0000-4000-8000-000000000002", name: "Old TV", createdAt: "2026-10-04T00:00:00Z" };
    let passwords: AppPassword[] = [phone];
    const server = settingsServer(on)
      .on("GET", `/api/v1/users/${regularUser.id}/app-passwords`, () => data(passwords))
      .on("POST", "/api/v1/users/me/app-passwords", () => {
        passwords = [phone, created];
        return json(201, { data: { appPassword: created, password: "abcd-efgh-ijkl-mnop-qrst-uvwx-yz23-4567" } });
      })
      .on("DELETE", `/api/v1/users/${regularUser.id}/app-passwords/:appPasswordId`, ({ params }) => {
        passwords = passwords.filter((entry) => entry.id !== params.appPasswordId);
        return noContent();
      });
    const { wrapper } = await open(server);
    const card = section(wrapper, "settings-app-passwords");
    expect(card.text()).toContain("cannot ask for a code");
    expect(card.find("tbody").text()).toContain("Phone");

    await control(wrapper, "Name").setValue("  Old TV  ");
    await card.find("form").trigger("submit");
    await flushPromises();
    expect(server.calls("POST", "/api/v1/users/me/app-passwords")[0]!.body).toEqual({ name: "Old TV" });
    expect(card.find("[data-app-password]").text()).toBe("abcd-efgh-ijkl-mnop-qrst-uvwx-yz23-4567");
    expect(card.text()).toContain("shown only once");
    expect(document.activeElement?.id).toBe("app-password-secret-title");
    expect(card.findAll("tbody tr")).toHaveLength(2);
    expect(card.find("tbody").text()).toContain("Never");
    await button(wrapper, "I have saved it").trigger("click");
    await flushPromises();
    expect(card.find("[data-app-password]").exists()).toBe(false);

    const row = card.findAll("tbody tr").find((candidate) => candidate.text().includes("Phone"))!;
    await row.find("button").trigger("click");
    expect(row.text()).toContain("every device signed in with it is signed out");
    await row.findAll("button").find((candidate) => candidate.text() === "Revoke now")!.trigger("click");
    await flushPromises();
    expect(server.calls("DELETE", `/api/v1/users/${regularUser.id}/app-passwords/${phone.id}`)).toHaveLength(1);
    expect(toastKeys(wrapper)).toContain("twoFactor.appPasswords.revoked");
    expect(card.find("tbody").text()).not.toContain("Phone");
  });

  it("checks the name and explains the limit", async () => {
    const server = settingsServer().on("POST", "/api/v1/users/me/app-passwords", () => apiError(409, "conflict"));
    const { wrapper } = await open(server);
    const card = section(wrapper, "settings-app-passwords");
    expect(card.text()).toContain("No application passwords yet.");
    await card.find("form").trigger("submit");
    expect(card.text()).toContain("Enter a name.");
    await control(wrapper, "Name").setValue("tab\there");
    await card.find("form").trigger("submit");
    expect(card.text()).toContain("contains control characters");
    expect(server.calls("POST", "/api/v1/users/me/app-passwords")).toHaveLength(0);
    await control(wrapper, "Name").setValue("Tablet");
    await card.find("form").trigger("submit");
    await flushPromises();
    expect(card.find("[role=alert]").text()).toBe("The account already has the maximum of 20 application passwords. Revoke one first.");
  });
});
