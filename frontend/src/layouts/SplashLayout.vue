<template>
  <div class="min-h-screen overflow-hidden flex login-bg">
    <div
      class="relative flex-1 flex flex-col justify-center py-12 px-4 sm:px-6 lg:flex-none lg:px-20 lg:w-1/2 xl:px-24" style="width:100%"
    >
      <router-view />
    </div>
  </div>
</template>

<script lang="ts">
import { computed } from "vue";
import { useRouter } from "vue-router";

export default {
  name: "SplashLayout",
  setup() {
    const router = useRouter();

    const route = computed(() => {
      return router.currentRoute.value.name;
    });

    return {
      route,
    };
  },
};
</script>

<style>
.login-bg{
  background: url(../assets/login-bg.jpg);
  background-size: 100% 100%;
}
</style>
