<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from "vue";
import { api } from "../../api/client";
import type { ChallengeDraft, GenerationJobResponse } from "../../api/types";
import "../../styles/dialog.css";
import "./authoring.css";

const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: []; published: [challengeId: string] }>();
const step = ref<"idea" | "draft" | "job">("idea");
const topic = ref("");
const draft = ref<ChallengeDraft>();
const verdict = ref("");
const reason = ref("");
const warnings = ref<string[]>([]);
const job = ref<GenerationJobResponse>();
const busy = ref(false);
const error = ref("");
let timer: number | undefined;
const ready = computed(
  () =>
    !!draft.value &&
    [
      draft.value.title,
      draft.value.description,
      draft.value.goal,
      draft.value.symptoms,
      draft.value.fault_mechanism,
      draft.value.environment_shape,
      draft.value.acceptance_criteria,
      draft.value.difficulty_reason,
    ].every((value) => value.trim()) &&
    draft.value.tags.length > 0,
);

function clearPoll() {
  if (timer !== undefined) window.clearTimeout(timer);
  timer = undefined;
}
function reset() {
  clearPoll();
  step.value = "idea";
  topic.value = "";
  draft.value = undefined;
  verdict.value = "";
  reason.value = "";
  warnings.value = [];
  job.value = undefined;
  busy.value = false;
  error.value = "";
}
watch(
  () => props.open,
  (open) => {
    if (open) reset();
    else clearPoll();
  },
);
onScopeDispose(clearPoll);

async function review() {
  if (topic.value.trim().length < 8) {
    error.value = "Please describe a concrete operational scenario.";
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    const result = await api.reviewGenerationDraft(topic.value.trim());
    draft.value = result.draft;
    verdict.value = result.verdict;
    reason.value = result.reason;
    warnings.value = result.warnings ?? [];
    step.value = "draft";
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Draft review failed";
  } finally {
    busy.value = false;
  }
}
function schedule(id: string) {
  clearPoll();
  timer = window.setTimeout(() => void poll(id), 2500);
}
async function poll(id: string) {
  try {
    job.value = await api.getGenerationJob(id);
    if (job.value.status === "success") {
      if (job.value.challenge_id) emit("published", job.value.challenge_id);
      return;
    }
    if (job.value.status === "failed") {
      error.value = job.value.message;
      return;
    }
    schedule(id);
  } catch (err) {
    error.value =
      err instanceof Error ? err.message : "Unable to read generation status";
  }
}
async function generate() {
  if (!draft.value || !ready.value) return;
  busy.value = true;
  error.value = "";
  try {
    job.value = await api.createGenerationJob(draft.value);
    step.value = "job";
    if (job.value.job_id) schedule(job.value.job_id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Generation failed";
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <section
      class="dialog authoring-dialog large"
      role="dialog"
      aria-modal="true"
    >
      <button
        class="icon-button dialog-close"
        aria-label="Close"
        @click="emit('close')"
      >
        x
      </button>
      <p class="eyebrow">Challenge authoring</p>
      <h2>Generate a challenge</h2>
      <template v-if="step === 'idea'"
        ><p class="dialog-copy">
          Describe a specific SRE scenario. The review agent will make the
          user-visible goals and acceptance criteria concrete before the
          generator builds the full challenge.
        </p>
        <textarea
          v-model="topic"
          rows="7"
          placeholder="Example: A nightly job should archive old application logs but leaves large files on disk because its cleanup logic is incomplete."
        ></textarea>
        <p v-if="error" class="form-error">{{ error }}</p>
        <button class="primary-button" :disabled="busy" @click="review">
          {{ busy ? "Reviewing..." : "Review idea" }}
        </button></template
      ><template v-else-if="step === 'draft' && draft"
        ><div class="draft-summary">
          <span :class="['difficulty', draft.difficulty]">{{
            draft.difficulty
          }}</span
          ><strong>{{ verdict }}</strong>
          <p>{{ reason }}</p>
        </div>
        <ul v-if="warnings.length" class="warning-list">
          <li v-for="warning in warnings" :key="warning">{{ warning }}</li>
        </ul>
        <div class="draft-fields">
          <label>Title<input v-model="draft.title" /></label
          ><label
            >Difficulty<select v-model="draft.difficulty">
              <option value="easy">easy</option>
              <option value="medium">medium</option>
              <option value="hard">hard</option>
            </select></label
          ><label class="span-all"
            >Tags (comma separated)<input
              :value="draft.tags.join(', ')"
              @input="
                draft.tags = ($event.target as HTMLInputElement).value
                  .split(',')
                  .map((value) => value.trim())
                  .filter(Boolean)
              " /></label
          ><label class="span-all"
            >Description<textarea v-model="draft.description" rows="2" /></label
          ><label>Goal<textarea v-model="draft.goal" rows="3" /></label
          ><label>Symptoms<textarea v-model="draft.symptoms" rows="3" /></label
          ><label
            >Fault mechanism<textarea
              v-model="draft.fault_mechanism"
              rows="3"
            /></label
          ><label
            >Environment shape<textarea
              v-model="draft.environment_shape"
              rows="3"
            /></label
          ><label
            >Acceptance criteria<textarea
              v-model="draft.acceptance_criteria"
              rows="3"
            /></label
          ><label
            >Difficulty reason<textarea
              v-model="draft.difficulty_reason"
              rows="3"
            /></label
          ><label class="span-all"
            >Notes<textarea v-model="draft.notes" rows="2" />
          </label>
        </div>
        <p v-if="error" class="form-error">{{ error }}</p>
        <div class="dialog-actions">
          <button class="compact-button" @click="step = 'idea'">Back</button
          ><button
            class="primary-button"
            :disabled="!ready || busy"
            @click="generate"
          >
            {{ busy ? "Starting..." : "Generate challenge" }}
          </button>
        </div></template
      ><template v-else-if="job"
        ><div class="job-panel" :data-status="job.status">
          <strong>{{ job.status }}</strong>
          <p>{{ error || job.message }}</p>
        </div>
        <button
          v-if="job.status === 'success'"
          class="primary-button"
          @click="emit('close')"
        >
          Done</button
        ><button
          v-else-if="job.status === 'failed'"
          class="compact-button"
          @click="step = 'draft'"
        >
          Back to draft
        </button></template
      >
    </section>
  </div>
</template>
