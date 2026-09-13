<script setup lang="ts">
import type { ScenarioContent } from "../../api/types";

defineProps<{ content: ScenarioContent }>();
</script>

<template>
  <article class="scenario-overview">
    <header>
      <p class="eyebrow">Reproducible operations scenario</p>
      <h2>Scenario overview</h2>
      <p class="scenario-overview-description">{{ content.description }}</p>
    </header>

    <section>
      <h3>Software and data</h3>
      <dl class="scenario-version-list">
        <template v-for="version in content.versions" :key="version.component">
          <dt>{{ version.component }}</dt>
          <dd>{{ version.version }}</dd>
        </template>
      </dl>
    </section>

    <section>
      <h3>Topology</h3>
      <p>{{ content.topology }}</p>
    </section>

    <section>
      <h3>Initialization</h3>
      <p>{{ content.initialization }}</p>
    </section>

    <section>
      <h3>Target phenomenon</h3>
      <p>{{ content.reproduction_objective }}</p>
    </section>

    <section>
      <h3>Key evidence</h3>
      <ul class="scenario-evidence-list">
        <li v-for="evidence in content.reproduction_evidence" :key="evidence.id">
          <div>
            <strong>{{ evidence.description }}</strong>
            <span v-if="evidence.node" class="scenario-evidence-node">{{ evidence.node }}</span>
          </div>
          <code>{{ evidence.id }}</code>
        </li>
      </ul>
    </section>
  </article>
</template>
