$(function() {
  $('.ui.dropdown').dropdown({ on: 'hover' });
  $('.ui.modal').modal();
  $('.ui.checkbox').checkbox();
  $('.tabular.menu .item').tab();

  // highlight table rows with mouseover using the warning color
   $('table tr').hover(function() {               
      $(this).addClass('warning');  
   }, function() {  
      $(this).removeClass('warning');  
   });  

  // fix for a bizarre double-submission problem that I totally don't understand
  var hasSubmitted = false;
  $('form').on('submit', function(e) {
    if (hasSubmitted) {
      e.preventDefault();
    } else {
      hasSubmitted = true;
    }
  });
  $('input').on('input', function(e) {
      hasSubmitted = false;
  });
});
