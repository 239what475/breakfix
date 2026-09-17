import { ref, watch, type Ref } from "vue";

// Small ref helpers keep prop booleans reactive across the admin pages
// without each page redeclaring the same watchers.
export function toActiveRef(source: { active: boolean }): Ref<boolean> {
	const active = ref(source.active);
	watch(
		() => source.active,
		(value) => {
			active.value = value;
		},
	);
	return active;
}

export function toLoggedInRef(source: { loggedIn: boolean }): Ref<boolean> {
	const loggedIn = ref(source.loggedIn);
	watch(
		() => source.loggedIn,
		(value) => {
			loggedIn.value = value;
		},
	);
	return loggedIn;
}
