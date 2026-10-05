import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { changePassword as changePasswordRequest, updateProfile, type Profile } from "@/features/settings/api";
import { useAuthStore } from "./auth";

// Account settings the signed-in user changes for themselves. The account
// itself stays in the auth store, which these actions keep current.
export const useSettingsStore = defineStore("settings", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const savingProfile = shallowRef(false);
  const changingPassword = shallowRef(false);

  async function saveProfile(profile: Profile): Promise<void> {
    savingProfile.value = true;
    try {
      auth.user = await updateProfile(client, profile);
    } finally {
      savingProfile.value = false;
    }
  }

  /** Changes the password; every session ends, so the user is signed out. */
  async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
    changingPassword.value = true;
    try {
      await changePasswordRequest(client, oldPassword, newPassword);
    } finally {
      changingPassword.value = false;
    }
    auth.expire();
  }

  return { savingProfile, changingPassword, saveProfile, changePassword };
});
