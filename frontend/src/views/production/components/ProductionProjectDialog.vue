<template>
  <t-dialog
    :visible="visible"
    width="500px"
    :header="t('production.projectDialog.title')"
    :confirm-btn="{ content: t('production.actions.create'), loading: submitting, disabled: !canCreate }"
    :cancel-btn="{ content: t('production.actions.cancel') }"
    :close-on-overlay-click="!submitting"
    :close-on-esc-keydown="!submitting"
    :on-confirm="submit"
    :on-close="close"
    @update:visible="updateVisible"
  >
    <t-alert v-if="!canCreate" theme="warning" :message="t('production.permissions.createDenied')" />
    <t-alert v-if="error" class="dialog-alert" theme="error" :message="error" />
    <t-form ref="formRef" class="project-form" :data="form" :rules="rules" label-align="top" @submit.prevent>
      <t-form-item :label="t('production.fields.projectName')" name="name">
        <t-input
          v-model="form.name"
          :placeholder="t('production.projectDialog.namePlaceholder')"
          :disabled="submitting || !canCreate"
          @enter="submit"
        />
      </t-form-item>
      <t-form-item :label="t('production.fields.description')" name="description">
        <t-textarea
          v-model="form.description"
          :maxlength="1000"
          :autosize="{ minRows: 3, maxRows: 6 }"
          :placeholder="t('production.projectDialog.descriptionPlaceholder')"
          :disabled="submitting || !canCreate"
        />
      </t-form-item>
    </t-form>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin, type FormInstanceFunctions } from 'tdesign-vue-next'
import { createProductionProject, type ProductionProject } from '@/api/production'
import { createProductionCommand } from '@/api/production/idempotency'
import { productionProjectNameRules } from '../models/productionViewModel'

const props = defineProps<{ visible: boolean; canCreate: boolean }>()
const emit = defineEmits<{
  (event: 'update:visible', value: boolean): void
  (event: 'created', project: ProductionProject): void
}>()

const { t } = useI18n()
const formRef = ref<FormInstanceFunctions | null>(null)
const submitting = ref(false)
const error = ref('')
const form = reactive({ name: '', description: '' })

const rules = computed(() => productionProjectNameRules(t))

watch(() => props.visible, (visible) => {
  if (!visible) return
  form.name = ''
  form.description = ''
  error.value = ''
  requestAnimationFrame(() => formRef.value?.clearValidate?.())
})

function updateVisible(value: boolean) {
  if (!value && submitting.value) return
  emit('update:visible', value)
}

function close() {
  if (!submitting.value) emit('update:visible', false)
}

async function submit() {
  if (submitting.value || !props.canCreate) return
  const result = await formRef.value?.validate?.()
  if (result !== true) return
  submitting.value = true
  error.value = ''
  try {
    const response = await createProductionProject(createProductionCommand({
      name: form.name.trim(),
      description: form.description.trim() || undefined,
    }))
    if (!response.success || !response.data) throw new Error(response.message || t('production.errors.createProject'))
    MessagePlugin.success(t('production.messages.projectCreated'))
    emit('created', response.data)
    emit('update:visible', false)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('production.errors.createProject')
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.dialog-alert { margin-bottom: 16px; }
.project-form { margin-top: 4px; }
.project-form :deep(.t-form__item:last-child) { margin-bottom: 0; }
</style>
