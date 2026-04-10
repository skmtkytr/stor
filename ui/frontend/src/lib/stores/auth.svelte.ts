import { api, getApiKey, setApiKey } from "$lib/rpc";

class AuthStore {
	authenticated = $state(false);
	loading = $state(false);
	error = $state("");

	async tryAutoAuth() {
		if (!getApiKey()) return;
		try {
			await api.version();
			this.authenticated = true;
		} catch {
			setApiKey("");
		}
	}

	async connect(key: string) {
		if (!key.trim()) return;
		this.loading = true;
		this.error = "";
		setApiKey(key.trim());
		try {
			await api.version();
			this.authenticated = true;
		} catch (e: unknown) {
			this.error = e instanceof Error ? e.message : "Connection failed";
			setApiKey("");
		} finally {
			this.loading = false;
		}
	}

	logout() {
		setApiKey("");
		this.authenticated = false;
	}
}

export const auth = new AuthStore();
